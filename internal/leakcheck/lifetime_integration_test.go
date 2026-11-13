//go:build linux && integration

package leakcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/compute/podman"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/store"
)

func realChecker(t *testing.T) (*Checker, context.Context, string) {
	t.Helper()
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires isolated privileged appliance and namespacehold fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := &Checker{Store: s, Runtime: podman.New("/run/podman/podman.sock"), Network: topology.New(), Registry: lifecycle.NewRegistry(), Gate: lifecycle.NewGate(), Processes: LinuxProcesses{}}
	ctx = lifecycle.WithRegistry(ctx, c.Registry)
	var name string
	for index := int64(80000000 + os.Getpid()%1000000); index < 99999999; index++ {
		name, _ = netns.Name(index)
		if _, err := os.Lstat("/run/netns/" + name); os.IsNotExist(err) {
			break
		}
	}
	if err := netns.Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netns.Delete(context.Background(), name); err != nil {
			t.Error(err)
		}
	})
	return c, ctx, name
}

func checkExclusive(ctx context.Context, t *testing.T, c *Checker) Result {
	t.Helper()
	var result Result
	if err := c.Gate.Exclusive(ctx, "default", func(ctx context.Context) error {
		var err error
		result, err = c.Check(ctx, "default")
		return err
	}); err != nil {
		t.Fatalf("required real inspection failed: %v (%+v)", err, result)
	}
	return result
}

func TestExternalFDHoldAfterUnlinkReportsMarkedResidue(t *testing.T) {
	c, ctx, name := realChecker(t)
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		return h.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "held0", Alias: "nephos:held-test"}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := netns.Do(ctx, name, func() error {
		return exec.CommandContext(ctx, "nft", "add", "table", "inet", "held", "{", "comment", "\"nephos:test-residue\"", ";", "}").Run()
	}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Getenv("NEPHOS_NAMESPACEHOLD"), name, "fd")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !reaped {
			_ = cmd.Wait()
		}
	})
	var ready struct {
		PID   int
		Inode uint64
		Mode  string
	}
	if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if err := netns.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	result := checkExclusive(ctx, t, c)
	if !result.Complete || result.Clean {
		t.Fatalf("external FD hold became clean: %+v", result)
	}
	fdFinding := false
	for _, f := range result.Findings {
		if f.Kind == "namespace-fd" {
			fdFinding = true
		}
	}
	if !fdFinding {
		t.Fatalf("independent FD absent from findings: %+v", result)
	}
	refs, err := netns.ObserveKnown(ctx, []uint64{ready.Inode})
	if err != nil || len(refs) == 0 {
		t.Fatalf("FD witness: %+v %v", refs, err)
	}
	// Independent tool observation through the external process's FD, not a
	// registry count or a named namespace path.
	for _, tool := range [][]string{{"ip", "-j", "link"}, {"nft", "-j", "list", "ruleset"}} {
		args := append([]string{"--net=" + refs[0].Path, "--"}, tool...)
		out, err := exec.CommandContext(ctx, "nsenter", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("independent kernel witness: %v %s", err, out)
		}
		want := "held0"
		if tool[0] == "nft" {
			want = "nephos:test-residue"
		}
		if !strings.Contains(string(out), want) {
			t.Fatalf("independent kernel residue %q absent: %s", want, out)
		}
		t.Logf("unlinked namespace kernel witness: %s", out)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	reaped = true
	if result := checkExclusive(ctx, t, c); !result.Clean {
		t.Fatalf("after external FD close/reap: %+v", result)
	}
}

func TestApplianceOwnedStateEmptyBeforeOuterTeardown(t *testing.T) {
	c, ctx, name := realChecker(t)
	if err := netns.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	result := checkExclusive(ctx, t, c)
	if !result.Complete || !result.Clean || len(c.Registry.Outstanding("default")) != 0 {
		t.Fatalf("inner proof must precede Docker teardown: %+v refs=%+v", result, c.Registry.Outstanding("default"))
	}
	t.Logf("still-running appliance: Complete=%v Clean=%v findings=%d inspectionErrors=%d registeredReferences=%d", result.Complete, result.Clean, len(result.Findings), len(result.InspectionErrors), len(c.Registry.Outstanding("default")))
}

func TestSocketOnlyHoldDemonstratesProcBlindSpotAndRegisteredSocketBlocks(t *testing.T) {
	c, ctx, name := realChecker(t)
	cmd := exec.CommandContext(ctx, os.Getenv("NEPHOS_NAMESPACEHOLD"), name, "socket")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !reaped {
			_ = cmd.Wait()
		}
	})
	var ready struct {
		PID   int
		Inode uint64
		Mode  string
	}
	if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if err := netns.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	refs, err := netns.ObserveKnown(ctx, []uint64{ready.Inode})
	if err != nil || len(refs) != 0 {
		t.Fatalf("socket-only witness retained a task/namespace FD: %+v %v", refs, err)
	}
	t.Logf("external socket-only witness PID=%d inode=%d has no task/NS-FD observation; this is not universal absence proof", ready.PID, ready.Inode)
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	reaped = true
	// The equivalent registered socket uses the same lifecycle accounting as
	// product netlink handles, but closes every explicit namespace FD first.
	if err := netns.Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	pin, err := vnetns.GetFromPath("/run/netns/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var identity unix.Stat_t
	if err := unix.Fstat(int(pin), &identity); err != nil {
		_ = pin.Close()
		t.Fatal(err)
	}
	c.Registry.Remember("default", "namespace", strconv.FormatUint(identity.Ino, 10))
	release := c.Registry.Track("default", "netlink-socket", fmt.Sprintf("test-inode:%d", identity.Ino), nil)
	h, err := netlink.NewHandleAt(pin)
	if err != nil {
		_ = pin.Close()
		release()
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			h.Close()
			release()
		}
	})
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := netns.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	refs, err = netns.ObserveKnown(ctx, []uint64{identity.Ino})
	if err != nil || len(refs) != 0 {
		t.Fatalf("registered socket is not socket-only: %+v %v", refs, err)
	}
	if links, err := h.LinkList(); err != nil || len(links) == 0 {
		t.Fatalf("registered socket did not retain actual kernel namespace: %+v %v", links, err)
	}
	result := checkExclusive(ctx, t, c)
	if !result.Complete || result.Clean {
		t.Fatalf("registered socket became clean: %+v", result)
	}
	h.Close()
	release()
	closed = true
	if result := checkExclusive(ctx, t, c); !result.Clean {
		t.Fatalf("actual socket close did not release references: %+v", result)
	}
}
