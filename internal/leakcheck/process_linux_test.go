package leakcheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Amrzxk/nephos/internal/lifecycle"
)

func TestKnownRuntimeCgroupsAndProcessesBlockClean(t *testing.T) {
	proc, groups := t.TempDir(), t.TempDir()
	id := strings.Repeat("a", 64)
	foreign := strings.Repeat("b", 64)
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(groups, "libpod-"+id+".scope", "cgroup.procs"), "42\n")
	write(filepath.Join(groups, "libpod-"+foreign+".scope", "cgroup.procs"), "43\n")
	write(filepath.Join(proc, "42", "task", "42", "cgroup"), "0::/libpod-"+id+".scope\n")
	write(filepath.Join(proc, "42", "cmdline"), "/usr/bin/conmon\x00-c\x00"+id+"\x00-e\x00")
	p := LinuxProcesses{procRoot: proc, cgroupRoot: groups, observeNamespaces: func(context.Context, []uint64) ([]Finding, error) { return nil, nil }}
	findings, err := p.Observe(context.Background(), []lifecycle.Reference{{Workspace: "default", Kind: "runtime", Identity: id}})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, f := range findings {
		if strings.Contains(f.Identity, foreign) {
			t.Fatalf("foreign runtime misreported: %+v", f)
		}
		kinds[f.Kind] = true
	}
	for _, kind := range []string{"cgroup", "runtime-task", "runtime-supervisor"} {
		if !kinds[kind] {
			t.Errorf("missing %s: %+v", kind, findings)
		}
	}
}

func TestRequiredCgroupInspectionCannotBecomeEmpty(t *testing.T) {
	p := LinuxProcesses{procRoot: t.TempDir(), cgroupRoot: filepath.Join(t.TempDir(), "missing"), observeNamespaces: func(context.Context, []uint64) ([]Finding, error) { return nil, nil }}
	if _, err := p.Observe(context.Background(), nil); err == nil {
		t.Fatal("missing required cgroup inspection accepted")
	}
}
