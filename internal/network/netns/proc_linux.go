package netns

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

// ProcessReference corroborates a known namespace identity. Socket-only holds
// are deliberately not claimed to be discoverable by this /proc observation.
type ProcessReference struct {
	Kind, Path string
	Inode      uint64
}

// ObserveKnown scans task (not only process-leader) namespaces and namespace
// FDs. Disappearing proc entries are benign; denied required inspection is not.
func ObserveKnown(ctx context.Context, known []uint64) ([]ProcessReference, error) {
	return observeKnown(ctx, "/proc", known, 0)
}

func observeKnown(ctx context.Context, root string, known []uint64, onlyPID int) ([]ProcessReference, error) {
	wanted := make(map[uint64]bool, len(known))
	for _, inode := range known {
		wanted[inode] = true
	}
	processes, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("enumerate proc processes: %w", err)
	}
	refs := make([]ProcessReference, 0)
	for _, process := range processes {
		pid, err := strconv.Atoi(process.Name())
		if err != nil || pid < 1 || (onlyPID != 0 && pid != onlyPID) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		taskDir := filepath.Join(root, process.Name(), "task")
		tasks, err := os.ReadDir(taskDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("enumerate %s: %w", taskDir, err)
		}
		for _, task := range tasks {
			base := filepath.Join(taskDir, task.Name())
			path := filepath.Join(base, "ns", "net")
			var stat unix.Stat_t
			if err := unix.Stat(path, &stat); errors.Is(err, unix.ENOENT) {
				continue
			} else if err != nil {
				return nil, fmt.Errorf("inspect task namespace %s: %w", path, err)
			}
			if wanted[stat.Ino] {
				refs = append(refs, ProcessReference{"task", path, stat.Ino})
			}
			fdDir := filepath.Join(base, "fd")
			fds, err := os.ReadDir(fdDir)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("enumerate namespace FDs %s: %w", fdDir, err)
			}
			for _, fd := range fds {
				path := filepath.Join(fdDir, fd.Name())
				if err := unix.Stat(path, &stat); errors.Is(err, unix.ENOENT) {
					continue
				} else if err != nil {
					return nil, fmt.Errorf("inspect namespace FD %s: %w", path, err)
				}
				if wanted[stat.Ino] {
					// A bind-mounted namespace FD's symlink is its mount path,
					// possibly with "(deleted)", not necessarily net:[inode].
					// Prove NSFS on the actual target instead of filtering text.
					var fs unix.Statfs_t
					if err := unix.Statfs(path, &fs); errors.Is(err, unix.ENOENT) {
						continue
					} else if err != nil {
						return nil, fmt.Errorf("inspect known namespace FD filesystem: %w", err)
					}
					if fs.Type != unix.NSFS_MAGIC {
						continue
					}
					refs = append(refs, ProcessReference{"namespace-fd", path, stat.Ino})
				}
			}
		}
	}
	return refs, nil
}

// WithObservedHandle pins an accessible, independently observed network
// namespace. The marker supplies only an identity to search for; NSFS/type,
// inode and non-root identity are proved on the descriptor actually used.
func WithObservedHandle(ctx context.Context, inode uint64, fn func(int, *netlink.Handle) error) error {
	refs, err := ObserveKnown(ctx, []uint64{inode})
	if err != nil {
		return err
	}
	var root unix.Stat_t
	if err := unix.Stat("/proc/1/ns/net", &root); err != nil {
		return fmt.Errorf("inspect root namespace: %w", err)
	}
	for _, ref := range refs {
		fd, err := unix.Open(ref.Path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("pin observed namespace %s: %w", ref.Path, err)
		}
		release := trackReference(ctx, "namespace-fd", fmt.Sprintf("observed-inode:%d/fd:%d", inode, fd))
		var stat unix.Stat_t
		var fs unix.Statfs_t
		kind, kindErr := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
		err = errors.Join(unix.Fstat(fd, &stat), unix.Fstatfs(fd, &fs), kindErr)
		if err == nil && (fs.Type != unix.NSFS_MAGIC || kind != unix.CLONE_NEWNET || stat.Ino != inode || (stat.Ino == root.Ino && stat.Dev == root.Dev)) {
			err = fmt.Errorf("observed namespace identity changed or is appliance root")
		}
		if err != nil {
			return errors.Join(err, closeTrackedFD(ctx, fd, release))
		}
		rememberNamespace(ctx, inode)
		err = withPinnedHandle(ctx, vnetns.NsHandle(fd), func(handle *netlink.Handle) error { return fn(fd, handle) })
		return errors.Join(err, closeTrackedFD(ctx, fd, release))
	}
	return fmt.Errorf("no accessible reference proves network namespace inode %d", inode)
}
