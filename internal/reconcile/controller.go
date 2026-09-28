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

type retryEntry struct {
	attempt int
	due     time.Time
}

// Controller owns a bounded, deduplicated acceleration queue. SQLite remains
// the source of truth; startup and periodic sweeps repair any dropped enqueue.
type Controller struct {
	store    *store.Store
	network  NetworkEngine
	interval time.Duration
	workMu   sync.Mutex
	mu       sync.Mutex
	pending  map[string]struct{}
	retries  map[string]retryEntry
	wake     chan struct{}
}

// New wires durable state and the network engine.
func New(s *store.Store, network NetworkEngine, interval time.Duration) *Controller {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Controller{store: s, network: network, interval: interval,
		pending: make(map[string]struct{}), retries: make(map[string]retryEntry), wake: make(chan struct{}, 1)}
}

// Enqueue hints that one VPC changed. A full resync remains authoritative.
func (c *Controller) Enqueue(vpcID string) {
	if vpcID == "" {
		return
	}
	c.mu.Lock()
	if len(c.pending) < queueLimit {
		c.pending[vpcID] = struct{}{}
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Controller) pop() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.pending {
		delete(c.pending, id)
		return id
	}
	return ""
}

func (c *Controller) retry(vpcID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.retries[vpcID]
	if len(c.retries) >= queueLimit && entry.attempt == 0 {
		return
	}
	entry.attempt++
	delay := retryBase
	for n := 1; n < entry.attempt && delay < retryMax; n++ {
		delay *= 2
		if delay > retryMax {
			delay = retryMax
		}
	}
	entry.due = time.Now().Add(delay)
	c.retries[vpcID] = entry
}

func (c *Controller) clearRetry(vpcID string) {
	c.mu.Lock()
	delete(c.retries, vpcID)
	c.mu.Unlock()
}

func (c *Controller) enqueueDueRetries() {
	now := time.Now()
	c.mu.Lock()
	for id, entry := range c.retries {
		if !entry.due.After(now) && len(c.pending) < queueLimit {
			c.pending[id] = struct{}{}
			entry.due = now.Add(retryMax)
			c.retries[id] = entry
		}
	}
	c.mu.Unlock()
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
			if err := c.reconcile(ctx, id); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
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
			if err := c.Sweep(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
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
		return err
	}
	var effectErr error
	if vpc.DeletionRequested {
		if len(subnets) != 0 {
			return fmt.Errorf("deleting VPC %s still has %d subnets", id, len(subnets))
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
	}
	return nil
}
