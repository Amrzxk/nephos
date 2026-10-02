//go:build linux && integration

package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
)

func realInstance(t *testing.T) (context.Context, *Client, compute.RuntimeID) {
	t.Helper()
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires the isolated privileged test appliance with its private Podman socket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	// This adapter contract deliberately runs without the production OCI hook:
	// there is no SQLite desired instance in a runtime-only test. Launch a
	// test-owned Unix service against the same local Podman store, with an
	// explicit empty hooks directory. The final appliance retains its hook.
	private := t.TempDir()
	hooks := filepath.Join(private, "hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(private, "podman.sock")
	service := exec.CommandContext(ctx, "podman", "--hooks-dir="+hooks, "system", "service", "--time", "0", "unix://"+socket)
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Process.Kill(); _ = service.Wait() })
	c := New(socket)
	var ready error
	for attempt := 0; attempt < 50; attempt++ {
		ready = c.EnsureImage(ctx, image)
		if ready == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if ready != nil {
		t.Fatalf("test-owned Podman service not ready: %v", ready)
	}
	if err := c.EnsureImage(ctx, image); err != nil {
		t.Fatal(err)
	}
	instance := model.Instance{ID: fmt.Sprintf("i-%017x", time.Now().UnixNano()), WorkspaceID: "default", InstanceType: "t3.micro"}
	id, err := c.Create(ctx, instance)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := c.Delete(cleanup, id); err != nil {
			t.Error(err)
		}
	})
	if again, err := c.Create(ctx, instance); err != nil || again != id {
		t.Fatalf("repeated create %q %v", again, err)
	}
	if err := c.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx, id); err != nil {
		t.Fatalf("repeated start: %v", err)
	}
	status, err := c.Inspect(ctx, id)
	if err != nil || !status.Running {
		t.Fatalf("start status %+v %v", status, err)
	}
	return ctx, c, id
}
func realCommand(ctx context.Context, t *testing.T, c *Client, id compute.RuntimeID, command []string, input []byte, tty bool) (stdout, stderr []byte, status int) {
	t.Helper()
	req := compute.ExecRequest{Command: command, TTY: tty}
	if tty {
		req.Rows = 24
		req.Cols = 80
	}
	session, err := c.Exec(ctx, id, req)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var out, errout []byte
	var outErr, errErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); out, outErr = io.ReadAll(session.Stdout()) }()
	go func() { defer wg.Done(); errout, errErr = io.ReadAll(session.Stderr()) }()
	if len(input) > 0 {
		if _, err := session.Stdin().Write(input); err != nil {
			t.Fatal(err)
		}
	}
	if !tty {
		if err := session.Stdin().Close(); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if outErr != nil || errErr != nil {
		t.Fatalf("output: %v %v stdout=%q stderr=%q", outErr, errErr, out, errout)
	}
	code, err := session.Wait(ctx)
	if err != nil {
		t.Fatalf("wait: %v out=%q stderr=%q", err, out, errout)
	}
	return out, errout, code
}
func TestPodmanAdapterReal(t *testing.T) {
	ctx, c, id := realInstance(t)
	out, errout, code := realCommand(ctx, t, c, id, []string{"/bin/sh", "-c", "cat; printf 'tail\\000'; printf 'error\\000' >&2; exit 7"}, []byte("input\x00"), false)
	if string(out) != "input\x00tail\x00" || string(errout) != "error\x00" || code != 7 {
		t.Fatalf("real command %q %q %d", out, errout, code)
	}
	out, errout, code = realCommand(ctx, t, c, id, []string{"/bin/sh", "-c", "test -t 1; printf terminal; printf error >&2; exit 7"}, nil, true)
	if string(out) != "terminalerror" || len(errout) != 0 || code != 7 {
		t.Fatalf("real TTY %q %q %d", out, errout, code)
	}
	for file, want := range map[string]string{"memory.max": "1073741824", "pids.max": "512", "cpu.max": "200000 100000", "/proc/sys/net/ipv4/ping_group_range": "0 65535"} {
		path := "/sys/fs/cgroup/" + file
		if strings.HasPrefix(file, "/") {
			path = file
		}
		got, _, code := realCommand(ctx, t, c, id, []string{"cat", path}, nil, false)
		if strings.Join(strings.Fields(string(got)), " ") != want || code != 0 {
			t.Errorf("%s=%q exit=%d want %q", file, got, code, want)
		}
	}
	out, _, code = realCommand(ctx, t, c, id, []string{"cat", "/proc/self/uid_map"}, nil, false)
	fields := strings.Fields(string(out))
	if len(fields) != 3 || fields[0] != "0" || fields[1] == "0" || fields[2] != "65536" || code != 0 {
		t.Fatalf("user mapping=%q", out)
	}
	out, _, _ = realCommand(ctx, t, c, id, []string{"cat", "/proc/1/status"}, nil, false)
	for _, line := range strings.Split(string(out), "\n") {
		if value, ok := strings.CutPrefix(line, "CapBnd:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			if caps&(1<<13) != 0 || caps&(1<<12) == 0 {
				t.Fatalf("NET_RAW added or NET_ADMIN absent: %x", caps)
			}
		}
	}
}

func TestPodmanAdapterRealResize(t *testing.T) {
	ctx, c, id := realInstance(t)
	session, err := c.Exec(ctx, id, compute.ExecRequest{Command: []string{"/bin/sh", "-c", "read -r ready; stty size; exit 7"}, TTY: true, Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := session.Resize(ctx, 40, 120); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Stdin().Write([]byte("ready\n")); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(session.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "40 120") {
		t.Fatalf("real PTY size: %q", out)
	}
	if code, err := session.Wait(ctx); err != nil || code != 7 {
		t.Fatalf("PTY exit %d %v", code, err)
	}
}

func TestInstanceTaskLimit(t *testing.T) {
	ctx, c, id := realInstance(t)
	helper, err := os.ReadFile("/run/task-limit-helper")
	if err != nil {
		t.Fatal("build and copy tests/fixtures/tasklimit helper into the test appliance:", err)
	}
	// Write through instance-scoped stdin to its shared writable root. PID 1
	// and exec have different /tmp mounts after systemd boot; no host mounts.
	_, errout, code := realCommand(ctx, t, c, id, []string{"/bin/sh", "-c", "cat > /root/nephos-task-limit-helper && chmod 0755 /root/nephos-task-limit-helper"}, helper, false)
	if code != 0 {
		t.Fatalf("install test helper: %d %s", code, errout)
	}
	// systemd's default init.scope limit is 15% of the aggregate limit (76).
	// A test-owned scope removes that stricter per-unit limit, not the
	// container's pids.max=512, so the aggregate boundary is exercised.
	out, errout, code := realCommand(ctx, t, c, id, []string{"systemd-run", "--quiet", "--scope", "--property=TasksMax=infinity", "/root/nephos-task-limit-helper"}, nil, false)
	if code != 0 {
		t.Fatalf("bounded task limit helper: exit=%d %s %s", code, out, errout)
	}
	var result struct {
		Limit, Baseline, Peak, After, Children int
		Refused, ExistingAlive                 bool
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if result.Limit != 512 || result.Baseline <= 0 || result.Peak != 512 || !result.Refused || !result.ExistingAlive || result.Children < 1 || result.After >= result.Peak {
		t.Fatalf("limits not enforced: %+v", result)
	}
	if status, err := c.Inspect(ctx, id); err != nil || !status.Running {
		t.Fatalf("instance did not survive: %+v %v", status, err)
	}
	got, _, code := realCommand(ctx, t, c, id, []string{"/bin/echo", "responsive"}, nil, false)
	if strings.TrimSpace(string(got)) != "responsive" || code != 0 {
		t.Fatal("daemon or exec unresponsive after limit test")
	}
}
