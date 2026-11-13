//go:build !windows

package appliance

import (
	"fmt"
	"os"
)

// checkPrivate rejects a credential file or directory that other local users
// can read: on Unix-like systems that is any group or other permission bit.
func checkPrivate(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %v)", info.Name(), info.Mode().Perm())
	}
	return nil
}

// restrictToOwner makes an open credential file owner-only.
func restrictToOwner(f *os.File) error {
	return f.Chmod(0o600)
}
