// Package reconcile continuously makes observed topology match SQLite state.
package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

const (
	defaultInterval = 60 * time.Second
	queueLimit      = 1024
	retryBase       = 100 * time.Millisecond
	retryMax        = 30 * time.Second
)

// NetworkEngine is the topology operation boundary used by the controller.
type NetworkEngine interface {
	EnsureVPC(context.Context, model.VPC, []model.Subnet) error
	DeleteVPC(context.Context, model.VPC) error
	ListVPCNames(context.Context) ([]string, error)
}

// Controller owns a bounded, deduplicated acceleration queue. SQLite remains
// the source of truth; startup and periodic sweeps repair any dropped enqueue.
type Controller struct {
	workQueue
	degradation
	gate      *lifecycle.Gate
	store     *store.Store
	network   NetworkEngine
	interval  time.Duration
	workMu    sync.Mutex
	converged func()
}

// New wires durable state and the network engine.
func New(s *store.Store, network NetworkEngine, interval time.Duration, gates ...*lifecycle.Gate) *Controller {
	if interval <= 0 {
		interval = defaultInterval
	}
	c := &Controller{workQueue: newWorkQueue(), store: s, network: network, interval: interval}
	if len(gates) > 0 {
		c.gate = gates[0]
	}
	return c
}

// OnConverged registers a callback run after any VPC topology converges, so
// that work blocked on network prerequisites (instances waiting for their
// subnet gateway) is retried immediately instead of at the next resync.
// It must be set before Run and must not block.
func (c *Controller) OnConverged(fn func()) { c.converged = fn }

// handle applies the controller error policy to one resource's failure.
func (c *Controller) handle(ctx context.Context, id string, err error) error {
	if err == nil {
		c.clear(id)
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if IsFatal(err) {
		return err
	}
	slog.Warn("VPC reconcile will retry after a transient failure", "vpc_id", id, "error", err)
	c.mark(id)
	c.retry(id)
	return nil
}

// Run processes hints and retries, with a full resync at the configured
// interval. The daemon performs an initial Sweep before reporting ready.
func (c *Controller) Run(ctx context.Context) error {
	resync := time.NewTicker(c.interval)
	defer resync.Stop()
	retries := time.NewTicker(retryBase)
	defer retries.Stop()
	for {
		for id := c.pop(); id != ""; id = c.pop() {
			if err := c.handle(ctx, id, c.reconcile(ctx, id)); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.wake:
		case <-retries.C:
			c.enqueueDueRetries()
		case <-resync.C:
			if err := c.Sweep(ctx); err != nil && !errors.Is(err, ErrNotAdmitted) {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				if IsFatal(err) {
					return err
				}
				slog.Warn("periodic VPC resync failed; next resync retries", "error", err)
			}
		}
	}
}

func (c *Controller) reconcile(ctx context.Context, id string) error {
	c.workMu.Lock()
	defer c.workMu.Unlock()
	return c.reconcileLocked(ctx, id)
}

func (c *Controller) reconcileLocked(ctx context.Context, id string) error {
	ctx, done, admitted, err := enterEffect(ctx, c.gate)
	if err != nil {
		return err
	}
	if !admitted {
		c.retry(id)
		return nil
	}
	defer done()
	vpc, err := c.store.GetVPC(ctx, "default", id)
	if errors.Is(err, sql.ErrNoRows) {
		c.clearRetry(id)
		return nil
	}
	if err != nil {
		return fmt.Errorf("load VPC %s: %w", id, err)
	}
	subnets, err := c.store.ListSubnetsByVPC(ctx, id)
	if err != nil {
		return fmt.Errorf("list subnets of VPC %s: %w", id, err)
	}
	var effectErr error
	if vpc.DeletionRequested {
		if len(subnets) != 0 {
			return fmt.Errorf("%w: deleting VPC %s still has %d subnets", errInvariant, id, len(subnets))
		}
		effectErr = c.network.DeleteVPC(ctx, vpc)
	} else {
		active := make([]model.Subnet, 0, len(subnets))
		for i := range subnets {
			subnet := &subnets[i]
			if !subnet.DeletionRequested {
				active = append(active, *subnet)
			}
		}
		effectErr = c.network.EnsureVPC(ctx, vpc, active)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := c.store.RecordTopology(ctx, vpc, subnets, effectErr); err != nil {
		if errors.Is(err, store.ErrStaleSnapshot) {
			c.Enqueue(id)
			return nil
		}
		return fmt.Errorf("persist VPC %s reconciliation: %w", id, err)
	}
	if effectErr != nil {
		slog.Warn("VPC reconciliation failed", "vpc_id", id, "generation", vpc.Generation, "error", effectErr)
		c.retry(id)
	} else {
		c.clearRetry(id)
		if c.converged != nil && !vpc.DeletionRequested {
			c.converged()
		}
	}
	return nil
}
