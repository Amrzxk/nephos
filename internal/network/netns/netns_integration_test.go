//go:build linux && integration

package netns

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func freshTestName(t *testing.T) string {
	t.Helper()
	for range 10 {
		n, err := rand.Int(rand.Reader, big.NewInt(90000000))
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("nx-vpc-%d", n.Int64()+10000000)
		_, err = os.Lstat(namespacePath(name))
		if os.IsNotExist(err) {
			return name
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("could not allocate an unused test namespace name")
	return ""
}

func inodeAt(t *testing.T, path string) uint64 {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	return stat.Ino
}

func rootRoutes(t *testing.T) []string {
	t.Helper()
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(routes))
	for i := range routes {
		out = append(out, routes[i].String())
	}
	slices.Sort(out)
	return out
}

func TestDoLeavesCallerNamespaceAndRootRoutesUntouched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := freshTestName(t)
	rootInode := inodeAt(t, "/proc/thread-self/ns/net")
	beforeRoutes := rootRoutes(t)
	if err := Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := Delete(context.Background(), name); err != nil {
			t.Error(err)
		}
	})
	targetInode := inodeAt(t, namespacePath(name))
	if err := Ensure(ctx, name); err != nil {
		t.Fatalf("idempotent Ensure: %v", err)
	}
	if got := inodeAt(t, namespacePath(name)); got != targetInode {
		t.Fatalf("Ensure replaced namespace inode %d with %d", targetInode, got)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := Do(ctx, name, func() error {
				var stat unix.Stat_t
				if err := unix.Stat("/proc/thread-self/ns/net", &stat); err != nil {
					return err
				}
				if stat.Ino != targetInode {
					return fmt.Errorf("entered inode %d, want %d", stat.Ino, targetInode)
				}
				return nil
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := inodeAt(t, "/proc/thread-self/ns/net"); got != rootInode {
		t.Fatalf("caller moved to namespace inode %d, want %d", got, rootInode)
	}
	if afterRoutes := rootRoutes(t); !slices.Equal(afterRoutes, beforeRoutes) {
		t.Fatalf("root routes changed: before=%v after=%v", beforeRoutes, afterRoutes)
	}
}

func TestEnsureFailsWithoutSysAdmin(t *testing.T) {
	const child = "NEPHOS_TEST_NO_SYSADMIN"
	if os.Getenv(child) == "1" {
		if err := Ensure(context.Background(), os.Getenv("NEPHOS_TEST_NAME")); err == nil {
			t.Fatal("unprivileged Ensure claimed success")
		}
		return
	}
	name := freshTestName(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestEnsureFailsWithoutSysAdmin$")
	cmd.Env = append(os.Environ(), child+"=1", "NEPHOS_TEST_NAME="+name)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: 65534, Gid: 65534},
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unprivileged test failed: %v\n%s", err, out)
	}
}
