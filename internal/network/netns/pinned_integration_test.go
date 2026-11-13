//go:build linux && integration

package netns

import (
	"context"
	"os"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestPinnedNamespaceIdentityCheckedBeforeMutation(t *testing.T) {
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires isolated privileged appliance")
	}
	ctx := context.Background()
	name := freshTestName(t)
	if err := Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Delete(ctx, name); err != nil {
			t.Error(err)
		}
	})
	inode := inodeAt(t, namespacePath(name))
	called := false
	if err := WithIdentityHandle(ctx, name, inode+1, func(*netlink.Handle) error { called = true; return nil }); err == nil || called {
		t.Fatalf("replacement identity authorized mutation: called=%v err=%v", called, err)
	}
	if err := WithIdentityHandle(ctx, name, inode, func(*netlink.Handle) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("original pin refused: called=%v err=%v", called, err)
	}
}
