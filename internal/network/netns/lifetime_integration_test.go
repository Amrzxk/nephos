//go:build linux && integration

package netns

import (
	"context"
	"os"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/lifecycle"
)

func TestNamespaceLifetimesRegisterBeforeExposureAndJoinDeferredCleanup(t *testing.T) {
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires isolated privileged appliance")
	}
	registry := lifecycle.NewRegistry()
	ctx := lifecycle.WithRegistry(context.Background(), registry)
	name := freshTestName(t)
	if err := Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Delete(ctx, name); err != nil {
			t.Error(err)
		}
	})
	bodyReturned := make(chan struct{})
	allowClose := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithHandle(ctx, name, func(*netlink.Handle) error {
			defer func() { close(bodyReturned); <-allowClose }()
			kinds := make(map[string]bool)
			for _, ref := range registry.Outstanding("default") {
				kinds[ref.Kind] = true
			}
			for _, kind := range []string{"namespace-fd", "namespace-worker", "netlink-socket"} {
				if !kinds[kind] {
					t.Errorf("%s exposed before lifetime registration", kind)
				}
			}
			return nil
		})
	}()
	<-bodyReturned
	if len(registry.Outstanding("default")) == 0 {
		t.Error("worker body result erased unfinished references")
	}
	select {
	case <-done:
		t.Error("namespace operation returned before deferred cleanup")
	default:
	}
	close(allowClose)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if refs := registry.Outstanding("default"); len(refs) != 0 {
		t.Fatalf("completed namespace operation retained references: %+v", refs)
	}
	if len(registry.Known("default")) == 0 {
		t.Fatal("closing references erased known namespace identity needed after unlink")
	}
}
