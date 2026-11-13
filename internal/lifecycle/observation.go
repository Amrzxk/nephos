package lifecycle

import (
	"context"
	"slices"
	"sort"
)

type registryContextKey struct{}

// WithRegistry carries daemon-owned reference accounting to lower adapters.
func WithRegistry(ctx context.Context, r *Registry) context.Context {
	return context.WithValue(ctx, registryContextKey{}, r)
}

// ContextRegistry returns internal accounting, never caller-supplied labels.
func ContextRegistry(ctx context.Context) *Registry {
	r, _ := ctx.Value(registryContextKey{}).(*Registry)
	return r
}

// Remember retains ephemeral observed identities after their handles close.
// This is corroborating evidence, never desired state or destructive authority.
func (r *Registry) Remember(workspace, kind, identity string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.known == nil {
		r.known = make(map[Reference]struct{})
	}
	r.known[Reference{workspace, kind, identity}] = struct{}{}
}

// Known includes previously closed identities needed to observe unlink leaks.
func (r *Registry) Known(workspace string) []Reference {
	r.mu.Lock()
	defer r.mu.Unlock()
	var refs []Reference
	for ref := range r.known {
		if ref.Workspace == workspace {
			refs = append(refs, ref)
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

// Errors preserves close/reap uncertainty even after a participant disappears.
func (r *Registry) Errors(workspace string) []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errors[workspace])
}

// Outstanding counts admitted effects, including children whose parent closed.
func (g *Gate) Outstanding(workspace string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.state(workspace).leases)
}

// IsExclusive is diagnostic; only the caller holding Exclusive can authorize
// success. Observing this bit is not transferable maintenance permission.
func (g *Gate) IsExclusive(workspace string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state(workspace).exclusive
}
