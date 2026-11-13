package hook

import (
	"context"

	"github.com/Amrzxk/nephos/internal/compute"
)

// Admission resolves verified hook identity to daemon-owned effect admission.
// It is invoked only after private peer, desired/runtime and namespace checks.
type Admission func(context.Context, compute.Reference) (func() error, error)
