package main

// Network namespace handling. This is the prototype of internal/network/netns,
// and it is the single most dangerous piece of code in Nephos (RISKS T8,
// ADR-0002).
//
// The hazard: the Go runtime moves goroutines between OS threads. setns changes
// the namespace of a *thread*. If a thread that switched namespace is ever
// returned to the scheduler, some unrelated goroutine will later run on it and
// create a socket, a netlink object, or an nftables rule in the wrong
// namespace — including the appliance root namespace. The failures are
// intermittent and extremely hard to attribute.
//
// The rule this file implements, and which the real package must keep:
//
//	A thread that switched namespace is never returned to the scheduler.
//
// Every namespace-entering call runs on its own locked thread, and that thread
// exits when the call returns. runtime.LockOSThread without a matching
// UnlockOSThread guarantees the runtime destroys the thread when the goroutine
// finishes, which is exactly what is wanted here.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const netnsDir = "/run/netns"

// Do runs fn inside the named network namespace.
//
// It never returns its thread to the scheduler. fn must not start goroutines
// that touch the namespace, because those would run on other threads in the
// original namespace.
func Do(name string, fn func() error) error {
	type result struct{ err error }
	done := make(chan result, 1)

	go func() {
		// Deliberately no matching UnlockOSThread: when this goroutine
		// returns, the runtime tears the thread down rather than reusing it.
		runtime.LockOSThread()

		origin, err := netns.Get()
		if err != nil {
			done <- result{fmt.Errorf("capturing the current network namespace: %w", err)}
			return
		}
		defer origin.Close()

		target, err := netns.GetFromPath(filepath.Join(netnsDir, name))
		if err != nil {
			done <- result{fmt.Errorf("opening network namespace %q: %w", name, err)}
			return
		}
		defer target.Close()

		if err := netns.Set(target); err != nil {
			done <- result{fmt.Errorf("entering network namespace %q: %w", name, err)}
			return
		}

		// Restoring is belt and braces. The thread is being destroyed anyway,
		// but if that ever changes, this keeps the invariant true.
		defer func() { _ = netns.Set(origin) }()

		done <- result{fn()}
	}()

	return (<-done).err
}

// CurrentNamespaceInode identifies the namespace the calling thread is in.
// Tests use it to prove a thread came back where it started (RISKS T8).
func CurrentNamespaceInode() (uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat("/proc/thread-self/ns/net", &st); err != nil {
		return 0, fmt.Errorf("stat of the current network namespace: %w", err)
	}
	return st.Ino, nil
}

// NamespaceInode identifies a named namespace.
func NamespaceInode(name string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(netnsDir, name), &st); err != nil {
		return 0, fmt.Errorf("stat of network namespace %q: %w", name, err)
	}
	return st.Ino, nil
}

// Create makes a named, persistent network namespace, replacing any existing
// one of that name. Persistent because everything must be reconstructible after
// a restart, and inspectable with `ip netns` while debugging (ADR-0007).
func Create(name string) error {
	if err := os.MkdirAll(netnsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", netnsDir, err)
	}
	path := filepath.Join(netnsDir, name)

	if err := Delete(name); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return fmt.Errorf("creating the mount point for namespace %q: %w", name, err)
	}
	_ = f.Close()

	// The new namespace must be created on a thread that is then discarded,
	// for the same reason Do discards its thread.
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()

		origin, err := netns.Get()
		if err != nil {
			errCh <- fmt.Errorf("capturing the current network namespace: %w", err)
			return
		}
		defer origin.Close()

		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			errCh <- fmt.Errorf("unshare(CLONE_NEWNET) for %q: %w", name, err)
			return
		}
		// Bind-mounting the thread's namespace onto the file is what makes it
		// outlive this process.
		if err := unix.Mount("/proc/thread-self/ns/net", path, "none", unix.MS_BIND, ""); err != nil {
			errCh <- fmt.Errorf("bind-mounting namespace %q: %w", name, err)
			return
		}
		errCh <- netns.Set(origin)
	}()

	if err := <-errCh; err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// Delete removes a named namespace if it exists.
func Delete(name string) error {
	path := filepath.Join(netnsDir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	if err := unix.Unmount(path, unix.MNT_DETACH); err != nil && err != unix.EINVAL {
		return fmt.Errorf("unmounting namespace %q: %w", name, err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing namespace %q: %w", name, err)
	}
	return nil
}

// List returns the Nephos-owned namespaces currently present. The prefix is
// what makes garbage collection safe: anything without it was not created by
// Nephos and is never touched (ADR-0007).
func List(prefix string) ([]string, error) {
	entries, err := os.ReadDir(netnsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s: %w", netnsDir, err)
	}
	var names []string
	for _, e := range entries {
		if len(prefix) == 0 || (len(e.Name()) >= len(prefix) && e.Name()[:len(prefix)] == prefix) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
