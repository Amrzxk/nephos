// Package lifecycle accounts for live effects and references. It is ephemeral
// observation only; SQLite remains the sole source of desired state.
package lifecycle

import (
	"context"
	"errors"
	"sync"
)

// ErrDraining means independent admission is closed for maintenance/shutdown.
var ErrDraining = errors.New("workspace effects are draining")

type workspaceGate struct {
	leases            map[*Lease]struct{}
	waiters           int
	exclusive, sealed bool
	maintenance       *exclusiveLease
}

type exclusiveLease struct {
	gate      *Gate
	workspace string
}
type exclusiveContextKey struct{}

// Gate serializes exclusive maintenance against fully completed effects.
type Gate struct {
	mu         sync.Mutex
	workspaces map[string]*workspaceGate
	changed    chan struct{}
}

// Lease owns one admitted effect. Children outlive their parent's close.
type Lease struct {
	gate      *Gate
	workspace string
	closed    bool // protected by gate.mu
}

// NewGate creates an empty effect barrier.
func NewGate() *Gate {
	return &Gate{workspaces: make(map[string]*workspaceGate), changed: make(chan struct{})}
}

func (g *Gate) state(workspace string) *workspaceGate {
	s := g.workspaces[workspace]
	if s == nil {
		s = &workspaceGate{leases: make(map[*Lease]struct{})}
		g.workspaces[workspace] = s
	}
	return s
}
func (g *Gate) notify() { close(g.changed); g.changed = make(chan struct{}) }
func (g *Gate) admit(workspace string) *Lease {
	l := &Lease{gate: g, workspace: workspace}
	g.state(workspace).leases[l] = struct{}{}
	return l
}

// Enter admits independent work, or fails without performing effects.
func (g *Gate) Enter(ctx context.Context, workspace string) (*Lease, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := g.state(workspace)
	if s.sealed || s.exclusive || s.waiters > 0 {
		return nil, ErrDraining
	}
	return g.admit(workspace), nil
}

// EnterChild requires an actual live lease from this gate. Callers must verify
// the child's runtime/namespace identity before obtaining its parent lease.
func (g *Gate) EnterChild(ctx context.Context, parent *Lease) (*Lease, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if parent == nil || parent.gate != g || parent.closed {
		return nil, errors.New("invalid effect parent lease")
	}
	if _, ok := g.state(parent.workspace).leases[parent]; !ok {
		return nil, errors.New("unregistered effect parent lease")
	}
	return g.admit(parent.workspace), nil
}

// Close releases only this participant, never its unfinished children.
func (l *Lease) Close() error {
	if l == nil || l.gate == nil {
		return errors.New("invalid effect lease")
	}
	g := l.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if !l.closed {
		delete(g.state(l.workspace).leases, l)
		l.closed = true
		g.notify()
	}
	return nil
}

// Seal permanently refuses independent work while allowing an admitted Start
// to finish its verified hook children. It does not erase outstanding effects.
func (g *Gate) Seal(workspace string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state(workspace).sealed = true
	g.notify()
}

// Exclusive closes independent admission, waits for every participant, and
// retains exclusivity through fn (including its final success transaction).
// Trusted maintenance must not recursively Enter this gate.
func (g *Gate) Exclusive(ctx context.Context, workspace string, fn func(context.Context) error) error {
	g.mu.Lock()
	s := g.state(workspace)
	s.waiters++
	g.notify()
	for {
		if err := ctx.Err(); err != nil {
			s.waiters--
			g.notify()
			g.mu.Unlock()
			return err
		}
		if len(s.leases) == 0 && !s.exclusive {
			break
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		g.mu.Lock()
	}
	s.waiters--
	s.exclusive = true
	lease := &exclusiveLease{gate: g, workspace: workspace}
	s.maintenance = lease
	g.notify()
	g.mu.Unlock()
	defer func() { g.mu.Lock(); s.exclusive = false; s.maintenance = nil; g.notify(); g.mu.Unlock() }()
	return fn(context.WithValue(ctx, exclusiveContextKey{}, lease))
}

// HoldsExclusive validates the actual current maintenance caller, not a
// snapshot bit or a saved context from an already completed maintenance run.
func (g *Gate) HoldsExclusive(ctx context.Context, workspace string) bool {
	lease, _ := ctx.Value(exclusiveContextKey{}).(*exclusiveLease)
	if lease == nil || lease.gate != g || lease.workspace != workspace {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state(workspace).maintenance == lease
}

type leaseContextKey struct{}

// WithLease carries an admitted controller effect to its runtime Start owner.
func WithLease(ctx context.Context, lease *Lease) context.Context {
	return context.WithValue(ctx, leaseContextKey{}, lease)
}

// ContextLease returns the internal admitted parent, never request input.
func ContextLease(ctx context.Context) *Lease {
	lease, _ := ctx.Value(leaseContextKey{}).(*Lease)
	return lease
}
