package config

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// verifyPrivateDir rejects a pre-existing runtime directory that the current user does not own,
// or whose DACL lets in any account other than the user, SYSTEM and the Administrators — the
// accounts that can read anything on the machine anyway, as root can on Unix (delta
// `2026-10-windows-port`, decision 2). A directory that does not exist yet is fine.
func verifyPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: inspect the runtime directory %s: %w", dir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config: %s exists and is not a directory", dir)
	}
	return verifyPrivateACL(dir)
}

// ProtectPrivateFile replaces path's access list with a protected one that admits the current
// user only, then reads it back. It is Windows' 0600: a token whose DACL was inherited from its
// directory is only as private as whatever that directory allows (REQ-SEC-003, REQ-WIN-001).
func ProtectPrivateFile(path string) error {
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + me.String() + ")")
	if err != nil {
		return fmt.Errorf("config: build the access list for %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("config: build the access list for %s: %w", path, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("config: restrict %s to the current user: %w", path, err)
	}
	return verifyPrivateACL(path)
}

// verifyPrivateACL refuses a path the current user does not own, or whose DACL lets in an
// account other than the user, SYSTEM and the Administrators.
func verifyPrivateACL(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("config: read the security of %s: %w", path, err)
	}
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(me) {
		return fmt.Errorf("config: %s is not owned by the current user", path)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("config: %s has no access list; refusing to put a token there", path)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("config: read the access list of %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafePointer(&ace.SidStart))
		if !trustedSID(sid, me) {
			return fmt.Errorf("config: %s lets in %s; refusing to put a token there", path, sid.String())
		}
	}
	return nil
}

// trustedSID is the user, SYSTEM or the Administrators group.
func trustedSID(sid, me *windows.SID) bool {
	if sid.Equals(me) {
		return true
	}
	for _, known := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		if s, err := windows.CreateWellKnownSid(known); err == nil && sid.Equals(s) {
			return true
		}
	}
	return false
}

// currentUserSID is the SID of the user this process runs as.
func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("config: read the current user: %w", err)
	}
	return u.User.Sid, nil
}

// VerifyPrivateFile refuses a file another account can reach, as a mode other than 0600 is
// refused on Unix.
func VerifyPrivateFile(path string) error { return verifyPrivateACL(path) }
