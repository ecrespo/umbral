// Package pty opens shells on a pseudo-terminal. On Windows that is ConPTY, which lands with
// layer 3 of T-F2-50 (delta `2026-10-windows-port`, decision 5); until then Open says so.
package pty

import (
	"errors"
	"os"

	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// ErrUnsupported is Open and Adopt on Windows until ConPTY is wired.
var ErrUnsupported = errors.New("pty: ConPTY is not wired yet")

// Open answers ErrUnsupported.
func Open(ports.PTYSpec) (ports.PTY, error) { return nil, ErrUnsupported }

// Adopt answers ErrUnsupported: a live handoff is not supported on Windows.
func Adopt(*os.File, int) (ports.PTY, error) { return nil, ErrUnsupported }
