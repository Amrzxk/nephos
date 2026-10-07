// namespacehold is a test-only external lifetime witness, never shipped in the
// appliance image. EOF releases the sole FD or socket and exits the process.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/network/netns"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: namespacehold NAME fd|socket")
	}
	name, mode := os.Args[1], os.Args[2]
	var identity unix.Stat_t
	if err := unix.Stat("/run/netns/"+name, &identity); err != nil {
		return err
	}
	var held int
	switch mode {
	case "fd":
		fd, err := unix.Open("/run/netns/"+name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		held = fd
	case "socket":
		if err := netns.Do(ctx, name, func() error {
			fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				return err
			}
			held = fd
			return nil
		}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown hold mode %q", mode)
	}
	defer func() { _ = unix.Close(held) }()
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		PID   int
		Inode uint64
		Mode  string
	}{os.Getpid(), identity.Ino, mode}); err != nil {
		return err
	}
	_, err := io.Copy(io.Discard, os.Stdin)
	return err
}
