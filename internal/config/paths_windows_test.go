package config

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestAPrivateFileAdmitsOnlyItsUser_REQ_WIN_001 is the Windows side of REQ-SEC-003's private
// runtime directory and token: a path every account can open is refused, and ProtectPrivateFile
// leaves it admitting the current user alone.
func TestAPrivateFileAdmitsOnlyItsUser_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:(A;OICI;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("open the directory to everyone: %v", err)
	}
	if err := verifyPrivateDir(dir); err == nil {
		t.Error("a runtime directory open to everyone was accepted; the token would be readable by anyone")
	}

	token := filepath.Join(dir, TokenFileName)
	if err := os.WriteFile(token, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPrivateFile(token); err == nil {
		t.Error("a token that inherited everyone's access was accepted")
	}
	if err := ProtectPrivateFile(token); err != nil {
		t.Fatalf("ProtectPrivateFile: %v", err)
	}
	if err := VerifyPrivateFile(token); err != nil {
		t.Errorf("a protected token was refused: %v", err)
	}

	// A conditional allow is refused whatever it names: the check does not read conditions.
	conditional := filepath.Join(t.TempDir(), "conditional")
	if err := os.Mkdir(conditional, 0o700); err != nil {
		t.Fatal(err)
	}
	csd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;GA;;;OW)(XA;;GA;;;WD;(Member_of {SID(BA)}))")
	if err != nil {
		t.Fatal(err)
	}
	cdacl, _, err := csd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(conditional, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, cdacl, nil); err != nil {
		t.Fatalf("give the directory a conditional entry: %v", err)
	}
	if err := verifyPrivateDir(conditional); err == nil {
		t.Error("a runtime directory with a conditional allow for everyone was accepted")
	}

	if err := verifyPrivateDir(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("an absent runtime directory was rejected: %v", err)
	}
}
