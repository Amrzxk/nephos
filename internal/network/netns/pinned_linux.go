package netns

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
)

// withPinnedHandle returns only after the socket is closed and the locked
// worker is restored to the appliance namespace. It never unlocks a thread
// which has switched namespaces; Go discards that thread on worker exit.
func withPinnedHandle(ctx context.Context, target vnetns.NsHandle, fn func(*netlink.Handle) error) error {
	return runInPinnedNamespace(ctx, target, func() error {
		release := trackReference(ctx, "netlink-socket", fmt.Sprintf("namespace-fd:%d", target))
		handle, err := netlink.NewHandleAt(target)
		if err != nil {
			release()
			return fmt.Errorf("open pinned namespace netlink handle: %w", err)
		}
		defer func() { handle.Close(); release() }()
		return fn(handle)
	})
}

func runInPinnedNamespace(ctx context.Context, target vnetns.NsHandle, fn func() error) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := vnetns.GetFromPath("/proc/1/ns/net")
	if err != nil {
		return fmt.Errorf("pin appliance namespace for worker return: %w", err)
	}
	releaseRoot := trackReference(ctx, "control-plane-namespace-fd", fmt.Sprintf("fd:%d", root))
	defer func() { result = errors.Join(result, closeTrackedFD(ctx, int(root), releaseRoot)) }()
	releaseWorker := trackReference(ctx, "namespace-worker", fmt.Sprintf("namespace-fd:%d", target))
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		var result error
		defer func() {
			if err := vnetns.Set(root); err != nil {
				reportCleanup(ctx, err)
				result = errors.Join(result, err)
			} else {
				releaseWorker()
			}
			done <- result
		}()
		if err := ctx.Err(); err != nil {
			result = err
			return
		}
		if err := vnetns.Set(target); err != nil {
			result = fmt.Errorf("enter pinned namespace: %w", err)
			return
		}
		result = fn()
	}()
	return <-done
}
