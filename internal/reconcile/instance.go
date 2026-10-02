package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

// InstanceNetwork checks committed prerequisites and removes one owned ENI.
type InstanceNetwork interface {
	Ready(context.Context, model.Instance) (bool, error)
	EnsureRunning(context.Context, model.Instance, compute.Reference, compute.Status) error
	DeleteENI(context.Context, model.VPC, model.ENI) error
}

// InstanceController converges SQLite instance intent into local runtime state.
type InstanceController struct {
	store    *store.Store
	network  InstanceNetwork
	runtime  compute.Runtime
	interval time.Duration
	workMu   sync.Mutex
	mu       sync.Mutex
	pending  map[string]struct{}
	retries  map[string]retryEntry
	wake     chan struct{}
}

// NewInstances creates a bounded controller; the daemon calls Sweep at startup.
func NewInstances(s *store.Store, network InstanceNetwork, runtime compute.Runtime, interval time.Duration) *InstanceController {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &InstanceController{store: s, network: network, runtime: runtime, interval: interval,
		pending: make(map[string]struct{}), retries: make(map[string]retryEntry), wake: make(chan struct{}, 1)}
}

// Enqueue accelerates convergence without making the queue authoritative.
func (c *InstanceController) Enqueue(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	if len(c.pending) < queueLimit {
		c.pending[id] = struct{}{}
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *InstanceController) pop() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.pending {
		delete(c.pending, id)
		return id
	}
	return ""
}
func (c *InstanceController) retry(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.retries[id]
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
	c.retries[id] = entry
}
func (c *InstanceController) clearRetry(id string) {
	c.mu.Lock()
	delete(c.retries, id)
	c.mu.Unlock()
}
func (c *InstanceController) enqueueDueRetries() {
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

// Run processes hints, bounded retries and periodic durable resync.
func (c *InstanceController) Run(ctx context.Context) error {
	resync := time.NewTicker(c.interval)
	defer resync.Stop()
	retries := time.NewTicker(retryBase)
	defer retries.Stop()
	for {
		for id := c.pop(); id != ""; id = c.pop() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := c.reconcile(ctx, id); err != nil {
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
				return err
			}
		}
	}
}

// Sweep repairs dropped hints and converges every committed instance.
func (c *InstanceController) Sweep(ctx context.Context) error {
	c.workMu.Lock()
	defer c.workMu.Unlock()
	after := ""
	for {
		page, err := c.store.ListInstancePage(ctx, "default", after, 100)
		if err != nil {
			return fmt.Errorf("list desired instances: %w", err)
		}
		if len(page) == 0 {
			return nil
		}
		for i := range page {
			if err := c.reconcileLocked(ctx, page[i].ID); err != nil {
				return err
			}
		}
		after = page[len(page)-1].ID
	}
}
func (c *InstanceController) reconcile(ctx context.Context, id string) error {
	c.workMu.Lock()
	defer c.workMu.Unlock()
	return c.reconcileLocked(ctx, id)
}
func (c *InstanceController) reconcileLocked(ctx context.Context, id string) error {
	instance, err := c.store.GetInstance(ctx, "default", id)
	if errors.Is(err, sql.ErrNoRows) {
		c.clearRetry(id)
		return nil
	}
	if err != nil {
		return fmt.Errorf("load instance %s: %w", id, err)
	}
	if instance.DeletionRequested {
		return c.terminate(ctx, instance)
	}
	ready, err := c.network.Ready(ctx, instance)
	if err != nil {
		return c.fail(ctx, instance, err)
	}
	if !ready {
		return nil // the next hint or durable resync checks prerequisites again
	}
	created, err := c.lookupOrCreate(ctx, instance)
	if err != nil {
		return c.fail(ctx, instance, err)
	}
	ref := created.Reference
	if err := c.store.RebindRuntimeID(ctx, instance.ID, instance.Generation, instance.RuntimeID, string(ref.ID)); err != nil {
		return c.createdDuringStateChange(ctx, instance, created, err)
	}
	// Read the committed runtime ID and desired generation again before Start;
	// createRuntime then sees exactly this identity in SQLite.
	instance, err = c.store.GetInstance(ctx, "default", id)
	if err != nil {
		return fmt.Errorf("reload instance %s after create: %w", id, err)
	}
	if instance.DeletionRequested || instance.RuntimeID != string(ref.ID) {
		c.Enqueue(id)
		return nil
	}
	status, err := c.runtime.Inspect(ctx, ref)
	if err != nil {
		return c.fail(ctx, instance, err)
	}
	if !status.Running {
		if err := c.runtime.Start(ctx, ref); err != nil {
			return c.fail(ctx, instance, err)
		}
		status, err = c.runtime.Inspect(ctx, ref)
		if err != nil {
			return c.fail(ctx, instance, err)
		}
		if !status.Running {
			return c.fail(ctx, instance, fmt.Errorf("runtime did not report a running instance after start"))
		}
	}
	// A running process is not proof of a converged ENI. Pin its observed
	// namespace and repair the route/policy even on the old running fast path.
	if err := c.network.EnsureRunning(ctx, instance, ref, status); err != nil {
		return c.fail(ctx, instance, err)
	}
	if err := c.store.RecordInstance(ctx, instance, model.InstanceRunning, "", true); err != nil {
		if errors.Is(err, store.ErrStaleSnapshot) {
			c.Enqueue(id)
			return nil
		}
		return err
	}
	c.clearRetry(id)
	return nil
}

func runtimeReference(instance model.Instance) compute.Reference {
	return compute.Reference{Identity: compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}, ID: compute.RuntimeID(instance.RuntimeID)}
}

func (c *InstanceController) createdDuringStateChange(ctx context.Context, instance model.Instance, created compute.CreateResult, recordErr error) error {
	ref := created.Reference
	if !created.Created {
		// Discovery is not allocation: a failed cache write cannot authorize
		// destroying a retained root. A later sweep reconciles current intent.
		if !errors.Is(recordErr, store.ErrStaleSnapshot) {
			return fmt.Errorf("record retained runtime %s: %w", instance.ID, recordErr)
		}
		c.Enqueue(instance.ID)
		return nil
	}
	if errors.Is(recordErr, store.ErrStaleSnapshot) {
		// A concurrent Terminate may have advanced the desired generation.
		// Retain the owned ID so a failed immediate Delete is retryable.
		if err := c.store.RetainTerminatingRuntimeID(ctx, instance.ID, string(ref.ID)); err == nil {
			if err := c.runtime.Delete(ctx, ref); err != nil {
				current, loadErr := c.store.GetInstance(ctx, "default", instance.ID)
				if loadErr != nil {
					return fmt.Errorf("cleanup raced runtime %s: %w (load: %w)", instance.ID, err, loadErr)
				}
				return c.fail(ctx, current, err)
			}
			c.Enqueue(instance.ID)
			return nil
		}
	}
	if err := c.runtime.Delete(ctx, ref); err != nil {
		return fmt.Errorf("record runtime %s: %w; cleanup failed: %w", instance.ID, recordErr, err)
	}
	c.Enqueue(instance.ID)
	return nil
}

func (c *InstanceController) terminate(ctx context.Context, instance model.Instance) error {
	// Even NULL or stale caches may have a retained container. Resolve only
	// the expected identity; never follow the cache into another managed root.
	ref, err := c.runtime.Lookup(ctx, runtimeReference(instance).Identity)
	if err == nil {
		if err := c.runtime.Delete(ctx, ref); err != nil {
			return c.fail(ctx, instance, err)
		}
	} else if !errors.Is(err, compute.ErrNotFound) {
		return c.fail(ctx, instance, err)
	}
	subnet, err := c.store.GetSubnet(ctx, instance.WorkspaceID, instance.SubnetID)
	if err != nil {
		return c.fail(ctx, instance, err)
	}
	vpc, err := c.store.GetVPC(ctx, instance.WorkspaceID, subnet.VPCID)
	if err != nil {
		return c.fail(ctx, instance, err)
	}
	if err := c.network.DeleteENI(ctx, vpc, instance.ENI); err != nil {
		return c.fail(ctx, instance, err)
	}
	if err := c.store.FinishInstanceTermination(ctx, instance); err != nil {
		if errors.Is(err, store.ErrStaleSnapshot) {
			c.Enqueue(instance.ID)
			return nil
		}
		return err
	}
	c.clearRetry(instance.ID)
	return nil
}

func (c *InstanceController) fail(ctx context.Context, instance model.Instance, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.store.RecordInstance(ctx, instance, model.InstanceFailed, cause.Error(), false); err != nil {
		if errors.Is(err, store.ErrStaleSnapshot) {
			c.Enqueue(instance.ID)
			return nil
		}
		return fmt.Errorf("persist instance %s failure: %w", instance.ID, err)
	}
	slog.Warn("instance reconciliation failed", "instance_id", instance.ID, "generation", instance.Generation, "error", cause)
	c.retry(instance.ID)
	return nil
}
