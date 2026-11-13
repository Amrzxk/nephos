//go:build linux

// Package procfs holds small helpers shared by the /proc readers in the
// runtime, network and leak-check packages.
package procfs

import (
	"errors"
	"io/fs"

	"golang.org/x/sys/unix"
)

// Gone reports that a process disappeared between listing /proc and reading
// one of its files. The kernel returns ENOENT when the directory is already
// gone and ESRCH when the task exits while the directory is still open; both
// mean the process was reaped, not that inspection failed.
func Gone(err error) bool {
	return err != nil && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH))
}
