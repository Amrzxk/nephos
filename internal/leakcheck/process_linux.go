package leakcheck

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/procfs"
)

// LinuxProcesses observes known retired identities, never generic libpod
// names. Findings are diagnostic, not authority to kill a process or remove
// a cgroup. Socket-only namespaces require product lifetime registration.
type LinuxProcesses struct {
	procRoot, cgroupRoot string
	observeNamespaces    func(context.Context, []uint64) ([]Finding, error)
}

// Observe requires proc and cgroup enumeration even when nothing was known.
func (p LinuxProcesses) Observe(ctx context.Context, known []lifecycle.Reference) ([]Finding, error) {
	proc, groups := p.procRoot, p.cgroupRoot
	if proc == "" {
		proc = "/proc"
	}
	if groups == "" {
		groups = "/sys/fs/cgroup"
	}
	runtimes := map[string]bool{}
	var inodes []uint64
	for _, ref := range known {
		switch ref.Kind {
		case "namespace":
			inode, err := strconv.ParseUint(ref.Identity, 10, 64)
			if err != nil || inode == 0 {
				return nil, fmt.Errorf("invalid known namespace identity %q", ref.Identity)
			}
			inodes = append(inodes, inode)
		case "runtime":
			if len(ref.Identity) != 64 || strings.Trim(ref.Identity, "0123456789abcdef") != "" {
				return nil, fmt.Errorf("invalid known runtime identity %q", ref.Identity)
			}
			runtimes[ref.Identity] = true
		}
	}
	observe := p.observeNamespaces
	if observe == nil {
		observe = namespaceFindings
	}
	findings, err := observe(ctx, inodes)
	if err != nil {
		return nil, err
	}
	if err := filepath.WalkDir(groups, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect cgroup %s: %w", path, walkErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && knownCgroup(entry.Name(), runtimes) {
			// The cgroup itself is residue even when cgroup.procs is empty.
			if _, err := os.ReadFile(filepath.Join(path, "cgroup.procs")); err != nil {
				return fmt.Errorf("inspect owned cgroup membership: %w", err)
			}
			findings = append(findings, Finding{Kind: "cgroup", Identity: path, Reason: "known runtime cgroup remains"})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	processes, err := os.ReadDir(proc)
	if err != nil {
		return nil, fmt.Errorf("enumerate required proc: %w", err)
	}
	for _, process := range processes {
		if _, err := strconv.Atoi(process.Name()); err != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		base := filepath.Join(proc, process.Name())
		tasks, err := os.ReadDir(filepath.Join(base, "task"))
		if procfs.Gone(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("enumerate runtime tasks: %w", err)
		}
		for _, task := range tasks {
			path := filepath.Join(base, "task", task.Name(), "cgroup")
			data, err := os.ReadFile(path)
			if procfs.Gone(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("inspect runtime task cgroup: %w", err)
			}
			for _, part := range strings.Split(strings.TrimSpace(string(data)), "/") {
				if knownCgroup(part, runtimes) {
					findings = append(findings, Finding{Kind: "runtime-task", Identity: path, Reason: "task retains known runtime cgroup"})
					break
				}
			}
		}
		data, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if procfs.Gone(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect runtime supervisor arguments: %w", err)
		}
		args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
		if len(args) < 2 || args[0] != "/usr/bin/conmon" {
			continue
		}
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "-c" && runtimes[args[i+1]] {
				findings = append(findings, Finding{Kind: "runtime-supervisor", Identity: base, Reason: "supervisor arguments reference known runtime; existence or ambiguous identity blocks clean"})
				break
			}
		}
	}
	return findings, nil
}

func knownCgroup(name string, runtimes map[string]bool) bool {
	id, ok := strings.CutPrefix(name, "libpod-")
	return ok && runtimes[strings.TrimSuffix(id, ".scope")]
}

func namespaceFindings(ctx context.Context, inodes []uint64) ([]Finding, error) {
	refs, err := netns.ObserveKnown(ctx, inodes)
	if err != nil {
		return nil, err
	}
	findings := make([]Finding, 0, len(refs))
	for _, ref := range refs {
		findings = append(findings, Finding{Kind: ref.Kind, Identity: ref.Path, Reason: fmt.Sprintf("known owned namespace inode %d remains referenced", ref.Inode)})
	}
	return findings, nil
}
