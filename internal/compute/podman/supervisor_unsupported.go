//go:build !linux

package podman

import (
	"context"
	"fmt"

	"github.com/Amrzxk/nephos/internal/compute"
)

func scanExecSupervisors(context.Context, compute.Reference, string) ([]execSupervisor, error) {
	return nil, fmt.Errorf("exec supervisor inspection requires the Linux appliance")
}
