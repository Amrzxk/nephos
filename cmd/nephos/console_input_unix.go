//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Poll the sole input reader without changing file flags or closing stdin.
// A bounded poll lets cancellation reap a worker waiting on an idle terminal.
func readConsoleFile(ctx context.Context, file *os.File, buf []byte) (int, error) {
	fd := int(file.Fd())
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("poll console stdin: %w", err)
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return 0, os.ErrClosed
		}
		read, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return read, fmt.Errorf("read console stdin: %w", err)
		}
		if read == 0 {
			return 0, io.EOF
		}
		return read, nil
	}
}
