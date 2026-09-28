//go:build linux && integration

package netns

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestWithInstanceHandlePinned(t *testing.T) {
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires the isolated privileged Nephos test appliance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before := inodeAt(t, "/proc/thread-self/ns/net")
	name := strings.Replace(freshTestName(t), "nx-vpc-", "instance-target-", 1)
	run := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	}
	out, err := run("run", "-d", "--name", name, "--label", "io.nephos.test=instance-target",
		"--userns=auto:size=65536", "--network=none", "--cap-add=NET_ADMIN", "--memory=1g", "--pids-limit=512", "nephos-ubuntu:dev", "/bin/sleep", "300")
	if err != nil {
		t.Fatalf("create test-owned target: %v %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	removed := false
	defer func() {
		if !removed {
			if out, err := run("rm", "-f", "--time=0", id); err != nil {
				t.Errorf("cleanup target: %v %s", err, out)
			}
		}
	}()
	out, err = run("inspect", "--format", "{{.State.Pid}}", id)
	if err != nil {
		t.Fatalf("inspect target: %v %s", err, out)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	target, err := OpenInstance(ctx, pid, id)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if target.Inode() == before {
		t.Fatal("instance target shares root namespace")
	}
	verify := func(*netlink.Handle) error {
		var stat unix.Stat_t
		if err := unix.Stat("/proc/thread-self/ns/net", &stat); err != nil {
			return err
		}
		if stat.Ino != target.Inode() {
			return fmt.Errorf("entered inode %d want %d", stat.Ino, target.Inode())
		}
		return nil
	}
	if err := WithInstanceHandle(ctx, target, verify); err != nil {
		t.Fatal(err)
	}
	if out, err := run("rm", "-f", "--time=0", id); err != nil {
		t.Fatalf("remove original process: %v %s", err, out)
	}
	removed = true
	// The process disappeared, but the pinned descriptor still identifies the
	// original namespace; no subsequent operation reopens a PID.
	if err := WithInstanceHandle(ctx, target, verify); err != nil {
		t.Fatalf("pinned namespace after process exit: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := WithInstanceHandle(ctx, target, func(*netlink.Handle) error { called = true; return nil }); err == nil || called {
		t.Fatal("closed target still entered")
	}
	if after := inodeAt(t, "/proc/thread-self/ns/net"); after != before {
		t.Fatalf("caller namespace changed %d -> %d", before, after)
	}
}
