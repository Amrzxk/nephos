package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openConsoleOutput(file *os.File) (*os.File, func() error, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Mode().IsRegular() {
		// Reopening would reset a redirected file's offset or append mode.
		return file, func() error { return nil }, nil
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	var owned *os.File
	var openErr error
	if err := conn.Control(func(fd uintptr) {
		// Unlike dup, procfs open creates an independent file description.
		// O_NONBLOCK therefore cannot change the caller's stream flags.
		owned, openErr = os.OpenFile(fmt.Sprintf("/proc/self/fd/%d", fd), os.O_WRONLY|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	}); err != nil {
		return nil, nil, err
	}
	if openErr != nil {
		return nil, nil, openErr
	}
	return owned, owned.Close, nil
}
