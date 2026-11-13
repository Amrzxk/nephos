//go:build windows

package appliance

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// checkPrivate cannot use Unix mode bits on Windows, where Go reports every
// writable file as 0666. Access is governed by the DACL that restrictToOwner
// applies when the credential is written, inside the per-user profile.
func checkPrivate(os.FileInfo) error { return nil }

// restrictToOwner replaces the file's DACL with a single protected entry that
// grants the current user full control, so inherited ACEs from the parent
// directory (for example a shared folder) cannot expose the token.
func restrictToOwner(f *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("identify current user: %w", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build owner-only ACL: %w", err)
	}
	return windows.SetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}
