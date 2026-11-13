package reconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/Amrzxk/nephos/internal/lifecycle"
)

func enterEffect(ctx context.Context, gate *lifecycle.Gate) (effectCtx context.Context, release func(), admitted bool, err error) {
	if gate == nil {
		return ctx, func() {}, true, nil
	}
	var lease *lifecycle.Lease
	if parent := lifecycle.ContextLease(ctx); parent != nil {
		lease, err = gate.EnterChild(ctx, parent)
	} else {
		lease, err = gate.Enter(ctx, "default")
	}
	if errors.Is(err, lifecycle.ErrDraining) {
		return ctx, func() {}, false, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx, func() {}, false, ctx.Err()
		}
		// An invalid or unregistered parent lease is a programming error.
		return ctx, func() {}, false, fmt.Errorf("%w: %w", errInvariant, err)
	}
	return lifecycle.WithLease(ctx, lease), func() { _ = lease.Close() }, true, nil
}
