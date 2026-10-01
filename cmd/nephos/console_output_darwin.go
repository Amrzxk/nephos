package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openConsoleOutput(file *os.File) (*os.File, func() error, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Mode().IsRegular() {
		return file, func() error { return nil }, nil
	}
	conn, err := file.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	var owned *os.File
	var flags, duplicate int
	var setupErr error
	if err := conn.Control(func(fd uintptr) {
		flags, setupErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if setupErr != nil {
			return
		}
		duplicate, setupErr = unix.Dup(int(fd))
		if setupErr != nil {
			return
		}
		unix.CloseOnExec(duplicate)
		if setupErr = unix.SetNonblock(duplicate, true); setupErr != nil {
			_ = unix.Close(duplicate)
			return
		}
		owned = os.NewFile(uintptr(duplicate), file.Name())
	}); err != nil {
		return nil, nil, err
	}
	if setupErr != nil {
		return nil, nil, setupErr
	}
	return owned, func() error {
		// Darwin has no Linux-style independent procfs reopen. Restore the
		// shared output flags after writers are reaped; never close stdio.
		_, restoreErr := unix.FcntlInt(uintptr(duplicate), unix.F_SETFL, flags)
		return errors.Join(restoreErr, owned.Close())
	}, nil
}
