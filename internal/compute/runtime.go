// Package compute defines the instance runtime boundary. Implementations never
// import the control-plane service or HTTP API.
package compute

import (
	"context"
	"errors"
	"io"

	"github.com/Amrzxk/nephos/internal/model"
)

// RuntimeID is an opaque identity supplied by the local container runtime.
type RuntimeID string

// Identity is the desired instance identity against which observations are checked.
type Identity struct {
	WorkspaceID string
	InstanceID  string
}

// Reference binds an observed, immutable runtime ID to its expected owner.
type Reference struct {
	Identity Identity
	ID       RuntimeID
}

// CreateResult distinguishes a newly allocated root from retained discovery.
type CreateResult struct {
	Reference Reference
	Created   bool
}

// ErrNotFound means authoritative inspection found no expected container.
// Inspection failures and ownership collisions must never return this error.
var ErrNotFound = errors.New("expected runtime container not found")

// Status is the observed runtime state, not desired SQLite state.
type Status struct {
	Running bool
	PID     int
}

// ExecRequest describes a command attached to an instance console.
type ExecRequest struct {
	Command    []string
	TTY        bool
	Rows, Cols uint16
}

// ExecSession owns one attached command stream and its eventual exit status.
type ExecSession interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Resize(ctx context.Context, rows, cols uint16) error
	Wait(ctx context.Context) (int, error)
	Close() error
}

// Runtime is the small container lifecycle boundary used by reconciliation.
type Runtime interface {
	EnsureImage(ctx context.Context, ref string) error
	Lookup(ctx context.Context, identity Identity) (Reference, error)
	Create(ctx context.Context, instance model.Instance) (CreateResult, error)
	Start(ctx context.Context, ref Reference) error
	Delete(ctx context.Context, ref Reference) error
	Inspect(ctx context.Context, ref Reference) (Status, error)
	Exec(ctx context.Context, ref Reference, req ExecRequest) (ExecSession, error)
}
