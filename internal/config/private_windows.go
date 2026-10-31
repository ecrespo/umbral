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
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("config: read the security of %s: %w", dir, err)
	}
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(me) {
		return fmt.Errorf("config: runtime directory %s is not owned by the current user", dir)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("config: runtime directory %s has no access list; refusing to put a token there", dir)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("config: read the access list of %s: %w", dir, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafePointer(&ace.SidStart))
		if !trustedSID(sid, me) {
			return fmt.Errorf("config: runtime directory %s lets in %s; refusing to put a token there", dir, sid.String())
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
