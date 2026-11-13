// Package netns is the only Nephos package allowed to switch Linux network
// namespaces. A goroutine that enters a namespace never unlocks its OS thread.
package netns

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	namespaceDir = "/run/netns"
	prefix       = "nx-vpc-"
	maxIndex     = 99999999
)

var lifecycleMu sync.Mutex

// Name derives a bounded VPC namespace name from a database-allocated index.
func Name(index int64) (string, error) {
	if index < 1 || index > maxIndex {
		return "", fmt.Errorf("VPC namespace index %d is outside 1..%d", index, maxIndex)
	}
	return prefix + strconv.FormatInt(index, 10), nil
}

func validateName(name string) error {
	if !strings.HasPrefix(name, prefix) || len(name) > 15 {
		return fmt.Errorf("refusing foreign or oversized network namespace %q", name)
	}
	indexPart := strings.TrimPrefix(name, prefix)
	index, err := strconv.ParseInt(indexPart, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid VPC namespace %q: %w", name, err)
	}
	canonical, err := Name(index)
	if err != nil {
		return err
	}
	if canonical != name {
		return fmt.Errorf("noncanonical VPC namespace %q", name)
	}
	return nil
}

func namespacePath(name string) string {
	return filepath.Join(namespaceDir, name)
}

func requireNamespaceMount(ctx context.Context, path string) error {
	identity, err := namespaceMountIdentity(ctx, path)
	if err != nil {
		return err
	}
	var root unix.Stat_t
	if err := unix.Stat("/proc/1/ns/net", &root); err != nil {
		return fmt.Errorf("inspect appliance root namespace: %w", err)
	}
	if identity.Ino == root.Ino && identity.Dev == root.Dev {
		return fmt.Errorf("refusing namespace alias to appliance root: %s", path)
	}
	entries, err := os.ReadDir(namespaceDir)
	if err != nil {
		return fmt.Errorf("inspect namespace alias candidates: %w", err)
	}
	for _, entry := range entries {
		other := namespacePath(entry.Name())
		if other == path || !strings.HasPrefix(entry.Name(), "nx-") {
			continue
		}
		// Required alias inspection must not become an empty desired set.
		// Never follow symlinks or grant deletion authority to malformed paths.
		stat, err := namespaceMountIdentity(ctx, other)
		if err != nil {
			return fmt.Errorf("inspect namespace alias candidate %s: %w", other, err)
		}
		if identity.Ino == stat.Ino && identity.Dev == stat.Dev {
			return fmt.Errorf("refusing namespace alias %s to %s", path, other)
		}
	}
	return nil
}

func namespaceMountIdentity(ctx context.Context, path string) (identity unix.Stat_t, result error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return identity, fmt.Errorf("inspect namespace mount %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return identity, fmt.Errorf("refusing symlink namespace mount %s", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return identity, fmt.Errorf("open namespace mount %s: %w", path, err)
	}
	release := trackReference(ctx, "namespace-fd", path)
	defer func() { result = errors.Join(result, closeTrackedFD(ctx, fd, release)) }()
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return identity, fmt.Errorf("stat namespace filesystem %s: %w", path, err)
	}
	if stat.Type != unix.NSFS_MAGIC {
		return identity, fmt.Errorf("refusing non-namespace mount %s (filesystem %#x)", path, stat.Type)
	}
	kind, err := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err != nil {
		return identity, fmt.Errorf("inspect namespace type %s: %w", path, err)
	}
	if kind != unix.CLONE_NEWNET {
		return identity, fmt.Errorf("refusing mount %s: not a network namespace", path)
	}
	if err := unix.Fstat(fd, &identity); err != nil {
		return identity, fmt.Errorf("stat namespace identity %s: %w", path, err)
	}
	return identity, nil
}

// Ensure preserves an existing VPC namespace and creates only an absent one.
// A regular file or symlink at the reserved path is never treated as ours.
func Ensure(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()

	if err := os.MkdirAll(namespaceDir, 0o755); err != nil {
		return fmt.Errorf("create network namespace directory: %w", err)
	}
	path := namespacePath(name)
	if _, err := os.Lstat(path); err == nil {
		return requireNamespaceMount(ctx, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect VPC namespace %s: %w", name, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0o444)
	if err != nil {
		return fmt.Errorf("create namespace mount point %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close namespace mount point %s: %w", name, err)
	}

	root, err := vnetns.GetFromPath("/proc/1/ns/net")
	if err != nil {
		return errors.Join(fmt.Errorf("pin appliance namespace: %w", err), os.Remove(path))
	}
	releaseRoot := trackReference(ctx, "control-plane-namespace-fd", fmt.Sprintf("fd:%d", root))
	// Do not expose completion until the worker has returned to the appliance
	// namespace. Discarding the locked thread alone is not a joined lifetime.
	releaseWorker := trackReference(ctx, "namespace-worker", name)
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // no UnlockOSThread: never reuse a switched thread
		var result error
		changed := false
		defer func() {
			if changed {
				if err := vnetns.Set(root); err != nil {
					reportCleanup(ctx, err)
					result = errors.Join(result, err)
				} else {
					releaseWorker()
				}
			} else {
				releaseWorker()
			}
			done <- result
		}()
		if err := ctx.Err(); err != nil {
			result = err
			return
		}
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			result = fmt.Errorf("unshare VPC namespace %s: %w", name, err)
			return
		}
		changed = true
		var identity unix.Stat_t
		if err := unix.Stat("/proc/thread-self/ns/net", &identity); err != nil {
			result = fmt.Errorf("observe created namespace: %w", err)
			return
		}
		rememberNamespace(ctx, identity.Ino)
		if err := unix.Mount("/proc/thread-self/ns/net", path, "none", unix.MS_BIND, ""); err != nil {
			result = fmt.Errorf("bind VPC namespace %s: %w", name, err)
			return
		}
	}()
	if err := errors.Join(<-done, closeTrackedFD(ctx, int(root), releaseRoot)); err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return errors.Join(err, fmt.Errorf("remove failed namespace mount point %s: %w", name, removeErr))
		}
		return err
	}
	return requireNamespaceMount(ctx, path)
}

// Do runs fn in a VPC namespace on a disposable locked OS thread. fn must not
// launch goroutines that assume they inherit its namespace.
func Do(ctx context.Context, name string, fn func() error) error {
	return withNamedPin(ctx, name, 0, func(target vnetns.NsHandle) error { return runInPinnedNamespace(ctx, target, fn) })
}

func withNamedPin(ctx context.Context, name string, inode uint64, fn func(vnetns.NsHandle) error) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := namespacePath(name)
	if err := requireNamespaceMount(ctx, path); err != nil {
		return err
	}
	return withInspectionPin(ctx, name, inode, func(target vnetns.NsHandle) error {
		if err := requireNamespaceMount(ctx, path); err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(target), &stat); err != nil {
			return err
		}
		rememberNamespace(ctx, stat.Ino)
		return fn(target)
	})
}

// WithInspectionHandle is for broad read-only inventory of named namespaces,
// including foreign names. It grants no ownership or deletion authority.
func WithInspectionHandle(ctx context.Context, name string, inode uint64, fn func(*netlink.Handle) error) error {
	return withInspectionPin(ctx, name, inode, func(target vnetns.NsHandle) error { return withPinnedHandle(ctx, target, fn) })
}

func withInspectionPin(ctx context.Context, name string, inode uint64, fn func(vnetns.NsHandle) error) (result error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return fmt.Errorf("invalid namespace inspection name %q", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := namespacePath(name)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("pin VPC namespace %s: %w", name, err)
	}
	release := trackReference(ctx, "namespace-fd", fmt.Sprintf("%s/fd:%d", name, fd))
	defer func() { result = errors.Join(result, closeTrackedFD(ctx, fd, release)) }()
	var stat unix.Stat_t
	var fs unix.Statfs_t
	kind, kindErr := unix.IoctlRetInt(fd, unix.NS_GET_NSTYPE)
	if err := errors.Join(unix.Fstat(fd, &stat), unix.Fstatfs(fd, &fs), kindErr); err != nil {
		return fmt.Errorf("inspect pinned namespace: %w", err)
	}
	identity, err := namespaceMountIdentity(ctx, path)
	if err != nil {
		return err
	}
	if kind != unix.CLONE_NEWNET || fs.Type != unix.NSFS_MAGIC || stat.Dev != identity.Dev || stat.Ino != identity.Ino || (inode != 0 && stat.Ino != inode) {
		return fmt.Errorf("namespace %s identity changed before operation", name)
	}
	return fn(vnetns.NsHandle(fd))
}

// WithHandle binds a netlink socket explicitly to the entered namespace.
func WithHandle(ctx context.Context, name string, fn func(*netlink.Handle) error) error {
	return WithIdentityHandle(ctx, name, 0, fn)
}

// WithIdentityHandle binds validation and effects to the observed namespace
// identity; a replaced mount cannot receive mutations before unlink refusal.
func WithIdentityHandle(ctx context.Context, name string, inode uint64, fn func(*netlink.Handle) error) error {
	return withNamedPin(ctx, name, inode, func(target vnetns.NsHandle) error { return withPinnedHandle(ctx, target, fn) })
}

// Delete removes only a valid VPC namespace mount and refuses foreign files.
func Delete(ctx context.Context, name string) error {
	return DeleteIdentity(ctx, name, 0)
}

// DeleteIdentity rejects a mount replaced since its pinned kernel cleanup.
// A zero inode is used only for ordinary desired-row namespace teardown.
func DeleteIdentity(ctx context.Context, name string, inode uint64) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	path := namespacePath(name)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect VPC namespace %s: %w", name, err)
	}
	if err := requireNamespaceMount(ctx, path); err != nil {
		return err
	}
	if inode != 0 {
		identity, err := namespaceMountIdentity(ctx, path)
		if err != nil {
			return err
		}
		if identity.Ino != inode {
			return fmt.Errorf("namespace %s was replaced before unlink", name)
		}
	}
	// A verified external pin must not prevent removing the named mount.
	// Callers clean owned kernel children first; the checker observes retained
	// references independently. Detach is not proof that the namespace died.
	if err := unix.Unmount(path, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount VPC namespace %s: %w", name, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove VPC namespace mount point %s: %w", name, err)
	}
	return nil
}

// List returns only Nephos VPC namespace mounts, in stable name order.
func List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(namespaceDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list VPC namespaces: %w", err)
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if validateName(entry.Name()) != nil {
			continue
		}
		if err := requireNamespaceMount(ctx, namespacePath(entry.Name())); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}
