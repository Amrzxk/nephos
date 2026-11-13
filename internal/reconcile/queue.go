package reconcile

import (
	"errors"
	"sync"
	"time"
)

// ErrNotAdmitted reports that a sweep did not run because the effect gate was
// draining for maintenance or shutdown. Callers that need a completed sweep,
// such as daemon readiness, must retry rather than treat it as success.
var ErrNotAdmitted = errors.New("reconcile sweep not admitted: effects are draining")

// errInvariant marks a broken internal invariant. Unlike store contention or
// engine failures it cannot heal by retrying, so it stops the controller.
var errInvariant = errors.New("reconcile invariant violated")

type retryEntry struct {
	attempt int
	due     time.Time
}

// workQueue is a bounded, deduplicated acceleration queue with exponential
// per-key retry. SQLite stays authoritative: periodic sweeps repair any hint
// the queue drops when it is full.
type workQueue struct {
	mu      sync.Mutex
	pending map[string]struct{}
	retries map[string]retryEntry
	wake    chan struct{}
}

func newWorkQueue() workQueue {
	return workQueue{pending: make(map[string]struct{}), retries: make(map[string]retryEntry), wake: make(chan struct{}, 1)}
}

// Enqueue hints that one resource changed.
func (q *workQueue) Enqueue(id string) {
	if id == "" {
		return
	}
	q.mu.Lock()
	if len(q.pending) < queueLimit {
		q.pending[id] = struct{}{}
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *workQueue) pop() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id := range q.pending {
		delete(q.pending, id)
		return id
	}
	return ""
}

func retryDelay(attempt int) time.Duration {
	delay := retryBase
	for n := 1; n < attempt && delay < retryMax; n++ {
		delay *= 2
		if delay > retryMax {
			delay = retryMax
		}
	}
	return delay
}

func (q *workQueue) retry(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.retries[id]
	if len(q.retries) >= queueLimit && entry.attempt == 0 {
		return
	}
	entry.attempt++
	entry.due = time.Now().Add(retryDelay(entry.attempt))
	q.retries[id] = entry
}

func (q *workQueue) clearRetry(id string) {
	q.mu.Lock()
	delete(q.retries, id)
	q.mu.Unlock()
}

func (q *workQueue) enqueueDueRetries() {
	now := time.Now()
	q.mu.Lock()
	for id, entry := range q.retries {
		if !entry.due.After(now) && len(q.pending) < queueLimit {
			q.pending[id] = struct{}{}
			entry.due = now.Add(retryMax)
			q.retries[id] = entry
		}
	}
	q.mu.Unlock()
}

// degradation tracks whether recent reconcile work hit transient failures
// (store contention, I/O) that are being retried rather than stopping the
// daemon. It is observation only and never authoritative state.
type degradation struct {
	mu     sync.Mutex
	failed map[string]struct{}
}

func (d *degradation) mark(id string) {
	d.mu.Lock()
	if d.failed == nil {
		d.failed = make(map[string]struct{})
	}
	d.failed[id] = struct{}{}
	d.mu.Unlock()
}

func (d *degradation) clear(id string) {
	d.mu.Lock()
	delete(d.failed, id)
	d.mu.Unlock()
}

// Degraded reports whether any resource is currently retrying after a
// transient controller-level failure.
func (d *degradation) Degraded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.failed) > 0
}

// IsFatal decides whether a reconcile error must stop the controller. Only
// cancellation and broken invariants are fatal; everything else is retried
// with backoff so one busy SQLite write or flaky inspection cannot take the
// whole appliance down.
func IsFatal(err error) bool {
	return errors.Is(err, errInvariant)
}
