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

func requireNamespaceMount(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect namespace mount %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink namespace mount %s", path)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return fmt.Errorf("stat namespace filesystem %s: %w", path, err)
	}
	if stat.Type != unix.NSFS_MAGIC {
		return fmt.Errorf("refusing non-namespace mount %s (filesystem %#x)", path, stat.Type)
	}
	return nil
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
		return requireNamespaceMount(path)
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

	// unshare is thread-local. The locked thread is deliberately discarded.
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // no UnlockOSThread: never reuse a switched thread
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			done <- fmt.Errorf("unshare VPC namespace %s: %w", name, err)
			return
		}
		if err := unix.Mount("/proc/thread-self/ns/net", path, "none", unix.MS_BIND, ""); err != nil {
			done <- fmt.Errorf("bind VPC namespace %s: %w", name, err)
			return
		}
		done <- nil
	}()
	if err := <-done; err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return errors.Join(err, fmt.Errorf("remove failed namespace mount point %s: %w", name, removeErr))
		}
		return err
	}
	return requireNamespaceMount(path)
}

// Do runs fn in a VPC namespace on a disposable locked OS thread. fn must not
// launch goroutines that assume they inherit its namespace.
func Do(ctx context.Context, name string, fn func() error) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := namespacePath(name)
	if err := requireNamespaceMount(path); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // no UnlockOSThread after setns
		target, err := vnetns.GetFromPath(path)
		if err != nil {
			done <- fmt.Errorf("open VPC namespace %s: %w", name, err)
			return
		}
		defer func() { _ = target.Close() }()
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		if err := vnetns.Set(target); err != nil {
			done <- fmt.Errorf("enter VPC namespace %s: %w", name, err)
			return
		}
		done <- fn()
	}()
	return <-done
}

// WithHandle binds a netlink socket explicitly to the entered namespace.
func WithHandle(ctx context.Context, name string, fn func(*netlink.Handle) error) error {
	return Do(ctx, name, func() error {
		current, err := vnetns.Get()
		if err != nil {
			return fmt.Errorf("capture entered namespace %s: %w", name, err)
		}
		defer func() { _ = current.Close() }()
		handle, err := netlink.NewHandleAt(current)
		if err != nil {
			return fmt.Errorf("open bound netlink handle for %s: %w", name, err)
		}
		defer handle.Close()
		return fn(handle)
	})
}

// Delete removes only a valid VPC namespace mount and refuses foreign files.
func Delete(ctx context.Context, name string) error {
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
	if err := requireNamespaceMount(path); err != nil {
		return err
	}
	if err := unix.Unmount(path, 0); err != nil {
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
		if err := requireNamespaceMount(namespacePath(entry.Name())); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}
