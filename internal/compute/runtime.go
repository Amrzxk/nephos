// Package compute defines the instance runtime boundary. Implementations never
// import the control-plane service or HTTP API.
package compute

import (
	"context"
	"io"

	"github.com/Amrzxk/nephos/internal/model"
)

// RuntimeID is an opaque identity supplied by the local container runtime.
type RuntimeID string

// Status is the observed runtime state, not desired SQLite state.
type Status struct{ Running bool }

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
	Create(ctx context.Context, instance model.Instance) (RuntimeID, error)
	Start(ctx context.Context, id RuntimeID) error
	Delete(ctx context.Context, id RuntimeID) error
	Inspect(ctx context.Context, id RuntimeID) (Status, error)
	Exec(ctx context.Context, id RuntimeID, req ExecRequest) (ExecSession, error)
}
