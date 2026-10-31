package config

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/windows"
)

// ErrPipeImpostor is a pipe served by a process of another user. A pipe's name is predictable,
// so another account can create it before the daemon does; a client that sent its token there
// would hand it over (REQ-WIN-001, REQ-SEC-003).
var ErrPipeImpostor = errors.New("config: the pipe is served by another user's process; refusing to send the token")

// VerifyPipeServer checks, before anything is written, that the process serving a dialled pipe
// runs as the current user.
func VerifyPipeServer(conn net.Conn) error {
	fd, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("%w: cannot read the pipe's handle", ErrPipeImpostor)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(fd.Fd()), &pid); err != nil {
		return fmt.Errorf("%w: %w", ErrPipeImpostor, err)
	}
	owner, err := processUserSID(pid)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPipeImpostor, err)
	}
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	if !owner.Equals(me) {
		return fmt.Errorf("%w: served as %s", ErrPipeImpostor, owner.String())
	}
	return nil
}

// processUserSID is the SID of the user a process runs as.
func processUserSID(pid uint32) (*windows.SID, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer func() { _ = token.Close() }()
	u, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}
