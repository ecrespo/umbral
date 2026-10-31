package api

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/ecrespo/umbral/internal/config"
)

// assertPrivate fails unless path's access list admits the current user only.
func assertPrivate(t *testing.T, path string) {
	t.Helper()
	if err := config.VerifyPrivateFile(path); err != nil {
		t.Errorf("%s is not private: %v", filepath.Base(path), err)
	}
}

// loosen lets every account read and write path, as a careless icacls would.
func loosen(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("D:(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("loosen the access list: %v", err)
	}
	if config.VerifyPrivateFile(path) == nil {
		t.Fatal("the loosened file still reads as private; the test proves nothing")
	}
}

// TestThePipeAndTheTokenAdmitOnlyTheirUser_REQ_WIN_001 is REQ-SEC-007 on Windows: the pipe's
// access list names the current user alone, and the token beside it is as private.
func TestThePipeAndTheTokenAdmitOnlyTheirUser_REQ_WIN_001(t *testing.T) {
	t.Parallel()

	s := testServer(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialDaemon(ctx, &net.Dialer{}, s.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	h, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		t.Fatal("the pipe connection exposes no handle")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(h.Fd()), windows.SE_KERNEL_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read the pipe's access list: %v", err)
	}
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("the pipe has no access list (%v): anyone could open it", err)
	}
	if dacl.AceCount == 0 {
		t.Fatal("the pipe's access list is empty")
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(me.User.Sid) {
			t.Errorf("the pipe admits %s, not only the current user", sid.String())
		}
	}

	assertPrivate(t, filepath.Join(filepath.Dir(s.SocketPath()), TokenFileName))
}

// TestAPipeAlreadyCreatedStopsTheDaemon_REQ_WIN_001: the pipe's name is predictable, so another
// process may create it first; the daemon's first instance is created exclusive and fails to
// bind rather than serving behind it (decision 3).
func TestAPipeAlreadyCreatedStopsTheDaemon_REQ_WIN_001(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "umbral", "umbral.sock")
	squatter, err := winio.ListenPipe(config.PipeName(socket), nil)
	if err != nil {
		t.Fatalf("pre-create the pipe: %v", err)
	}
	defer func() { _ = squatter.Close() }()

	ln, err := bind(t.Context(), Config{SocketPath: socket})
	if err == nil {
		_ = ln.Close()
		t.Fatal("the daemon bound a pipe another process had already created")
	}
}
