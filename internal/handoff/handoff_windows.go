// Package handoff is the channel a newer umbrald takes a running one's sessions over
// (REQ-HND-001). Windows has no SCM_RIGHTS, and a live handoff there is a later task (delta
// `2026-10-windows-port`, decision 11): every entry point here answers ErrUnsupported, so the
// daemon refuses `--takeover` in words and never listens for one.
package handoff

import (
	"context"
	"errors"
	"os"
	"time"
)

// SocketName is the handoff socket's name in the daemon's runtime directory.
const SocketName = "handoff.sock"

// MaxFrame bounds one frame.
const MaxFrame = 64 << 20

// ErrPeer is a connection from a process of another user.
var ErrPeer = errors.New("handoff: the peer is not this daemon's user")

// ErrUnsupported is every handoff call on Windows.
var ErrUnsupported = errors.New("handoff: live handoff is not supported on Windows")

// Listener accepts handoff connections; on Windows there is none.
type Listener struct{}

// Listen answers ErrUnsupported.
func Listen(string) (*Listener, error) { return nil, ErrUnsupported }

// Accept answers ErrUnsupported.
func (*Listener) Accept() (*Conn, error) { return nil, ErrUnsupported }

// Close does nothing.
func (*Listener) Close() error { return nil }

// Abandon does nothing.
func (*Listener) Abandon() {}

// Conn is one handoff connection; on Windows there is none.
type Conn struct{}

// PeerPID is zero.
func (*Conn) PeerPID() int { return 0 }

// Dial answers ErrUnsupported.
func Dial(context.Context, string) (*Conn, error) { return nil, ErrUnsupported }

// SetDeadline answers ErrUnsupported.
func (*Conn) SetDeadline(time.Time) error { return ErrUnsupported }

// Close does nothing.
func (*Conn) Close() error { return nil }

// Send answers ErrUnsupported.
func (*Conn) Send(any, ...*os.File) error { return ErrUnsupported }

// SendRaw answers ErrUnsupported.
func (*Conn) SendRaw([]byte) error { return ErrUnsupported }

// Recv answers ErrUnsupported.
func (*Conn) Recv(any) ([]*os.File, error) { return nil, ErrUnsupported }
