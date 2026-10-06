package lifecycle

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// Reference describes an outstanding product-owned effect or handle.
type Reference struct{ Workspace, Kind, Identity string }
type registration struct {
	reference Reference
	cancel    context.CancelFunc
	canceled  bool
}

// Registry retains references until their real cleanup finishes, not until
// cancellation is requested. Entries are unique even for the same instance.
type Registry struct {
	mu       sync.Mutex
	entries  map[*registration]struct{}
	draining map[string]bool
	errors   map[string][]error
	changed  chan struct{}
}

// NewRegistry creates an empty observation registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[*registration]struct{}), draining: make(map[string]bool), errors: make(map[string][]error), changed: make(chan struct{})}
}
func (r *Registry) notify() { close(r.changed); r.changed = make(chan struct{}) }

// Track registers before exposing a reference. release follows actual close,
// reap and joined workers. Cancellation functions must be nonblocking.
func (r *Registry) Track(workspace, kind, identity string, cancel context.CancelFunc) (release func()) {
	entry := &registration{reference: Reference{workspace, kind, identity}, cancel: cancel}
	r.mu.Lock()
	r.entries[entry] = struct{}{}
	shouldCancel := r.draining[workspace]
	entry.canceled = shouldCancel
	r.notify()
	r.mu.Unlock()
	if shouldCancel && cancel != nil {
		cancel()
	}
	var once sync.Once
	return func() { once.Do(func() { r.mu.Lock(); delete(r.entries, entry); r.notify(); r.mu.Unlock() }) }
}

// ReportError retains cleanup failures so an empty registry cannot imply a
// successful drain after a failed close/reap.
func (r *Registry) ReportError(workspace string, err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors[workspace] = append(r.errors[workspace], err)
	r.notify()
}

// Outstanding returns a stable snapshot; duplicate entries remain visible.
func (r *Registry) Outstanding(workspace string) []Reference {
	r.mu.Lock()
	defer r.mu.Unlock()
	var refs []Reference
	for entry := range r.entries {
		if entry.reference.Workspace == workspace {
			refs = append(refs, entry.reference)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		return refs[i].Identity < refs[j].Identity
	})
	return refs
}

// Drain cancels every owned reference in this workspace and waits for release.
// A timeout leaves live entries intact for diagnostics and fail-closed checks.
func (r *Registry) Drain(ctx context.Context, workspace string) error {
	r.mu.Lock()
	r.draining[workspace] = true
	for {
		var cancels []context.CancelFunc
		count := 0
		for entry := range r.entries {
			if entry.reference.Workspace != workspace {
				continue
			}
			count++
			if !entry.canceled {
				entry.canceled = true
				if entry.cancel != nil {
					cancels = append(cancels, entry.cancel)
				}
			}
		}
		if count == 0 {
			err := errors.Join(r.errors[workspace]...)
			r.mu.Unlock()
			return err
		}
		changed := r.changed
		r.mu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		r.mu.Lock()
	}
}
