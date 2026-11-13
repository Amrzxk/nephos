//go:build linux

package procfs

import (
	"fmt"
	"io/fs"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGoneCoversBothReapTimings(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		gone bool
	}{
		"directory gone":         {err: &fs.PathError{Op: "open", Path: "/proc/1/comm", Err: unix.ENOENT}, gone: true},
		"task gone during read":  {err: &fs.PathError{Op: "read", Path: "/proc/1/comm", Err: unix.ESRCH}, gone: true},
		"wrapped":                {err: fmt.Errorf("inspect: %w", &fs.PathError{Op: "read", Err: unix.ESRCH}), gone: true},
		"bare stat ENOENT":       {err: unix.ENOENT, gone: true},
		"permission is not gone": {err: &fs.PathError{Op: "open", Path: "/proc/1/comm", Err: unix.EACCES}},
		"no error":               {},
	} {
		if got := Gone(tt.err); got != tt.gone {
			t.Errorf("%s: Gone=%v want %v", name, got, tt.gone)
		}
	}
}
