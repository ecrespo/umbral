// Package handoff is the channel a newer umbrald takes a running one's sessions over
// (REQ-HND-001, delta `2026-10-pty-handoff`): a Unix socket beside the API's, carrying
// length-prefixed JSON frames and, beside a frame, open descriptors as SCM_RIGHTS.
//
// It is a leaf on the standard library and x/sys: it moves bytes and descriptors and knows
// nothing of what they are. The steps and what each frame holds belong to cmd/umbrald, the
// only place allowed to reach every module a handoff touches.
package handoff

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// SocketName is the handoff socket's name in the daemon's runtime directory.
const SocketName = "handoff.sock"

// MaxFrame bounds one frame. A session's snapshot carries its scrollback, which is the
// largest thing a frame holds; the bound refuses a corrupt length before allocating it.
const MaxFrame = 64 << 20

// maxFiles is the most descriptors one frame carries: the listener and two locks.
const maxFiles = 4

// socketMode is the handoff socket's mode: only the daemon's own user may connect, and a peer
// of another uid is refused even then (Accept).
const socketMode = 0o600

// ErrPeer is a connection from a process of another user.
var ErrPeer = errors.New("handoff: the peer is not this daemon's user")

// Listener accepts handoff connections.
type Listener struct {
	ln *net.UnixListener
}

// Listen opens the handoff socket at path, replacing one a daemon that is gone left behind.
// The caller holds the instance lock, so nothing alive owns that path.
func Listen(path string) (*Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("handoff: remove the stale socket: %w", err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("handoff: listen on %s: %w", path, err)
	}
	// net.Listen applies the umask, so the mode is set rather than assumed.
	if err := os.Chmod(path, socketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("handoff: set the socket's mode: %w", err)
	}
	return &Listener{ln: ln}, nil
}

// Accept waits for the next connection and refuses one from another user.
func (l *Listener) Accept() (*Conn, error) {
	for {
		c, err := l.ln.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, pid, err := peer(c)
		if err == nil && uid == os.Getuid() {
			return &Conn{c: c, pid: pid}, nil
		}
		_ = c.Close()
	}
}

// Close stops listening and removes the socket.
func (l *Listener) Close() error { return l.ln.Close() }

// Abandon stops listening and leaves the socket's path where it is, for a daemon that hands
// its runtime directory over (the next one replaces the path at its own start).
func (l *Listener) Abandon() {
	l.ln.SetUnlinkOnClose(false)
	_ = l.ln.Close()
}

// Conn is one handoff connection.
type Conn struct {
	c *net.UnixConn
	// pid is the accepted peer's, from the kernel; zero on a dialled connection.
	pid int
}

// PeerPID is the pid of the process that connected, as the kernel recorded it at connect.
func (c *Conn) PeerPID() int { return c.pid }

// Dial connects to the handoff socket at path.
func Dial(ctx context.Context, path string) (*Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("handoff: %s is not a Unix socket", path)
	}
	return &Conn{c: uc}, nil
}

// SetDeadline bounds every read and write until it is changed.
func (c *Conn) SetDeadline(t time.Time) error { return c.c.SetDeadline(t) }

// Close closes the connection. Descriptors received stay open: they are the caller's.
func (c *Conn) Close() error { return c.c.Close() }

// Send writes v as one frame, with files passed beside it. The files stay open on this side;
// the receiver gets its own descriptors for the same open files.
func (c *Conn) Send(v any, files ...*os.File) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("handoff: encode a frame: %w", err)
	}
	if len(body) > MaxFrame {
		return fmt.Errorf("handoff: a frame of %d bytes is past the %d limit", len(body), MaxFrame)
	}
	if len(files) > maxFiles {
		return fmt.Errorf("handoff: %d descriptors in one frame, at most %d", len(files), maxFiles)
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)

	var oob []byte
	if len(files) > 0 {
		fds, err := descriptors(files)
		defer func() {
			for _, fd := range fds {
				_ = unix.Close(fd)
			}
		}()
		if err != nil {
			return err
		}
		oob = syscall.UnixRights(fds...)
	}
	// The rights travel with the first byte; the rest of a large frame follows as plain writes.
	n, _, err := c.c.WriteMsgUnix(frame, oob, nil)
	if err != nil {
		return fmt.Errorf("handoff: send a frame: %w", err)
	}
	if n < len(frame) {
		if _, err := c.c.Write(frame[n:]); err != nil {
			return fmt.Errorf("handoff: send a frame: %w", err)
		}
	}
	return nil
}

// SendRaw writes bytes as they are, for a test that needs a malformed frame.
func (c *Conn) SendRaw(p []byte) error {
	_, err := c.c.Write(p)
	return err
}

// Recv reads one frame into v and returns the descriptors that came with it. A peer that
// closed the connection between frames is io.EOF.
func (c *Conn) Recv(v any) ([]*os.File, error) {
	var header [4]byte
	oob := make([]byte, syscall.CmsgSpace(maxFiles*4))
	var files []*os.File

	got := 0
	for got < len(header) {
		n, oobn, _, _, err := c.c.ReadMsgUnix(header[got:], oob)
		if oobn > 0 {
			received, perr := parseRights(oob[:oobn])
			files = append(files, received...)
			if perr != nil {
				closeAll(files)
				return nil, perr
			}
		}
		got += n
		if err != nil {
			closeAll(files)
			if errors.Is(err, io.EOF) && got > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if n == 0 {
			closeAll(files)
			if got == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		}
	}

	size := binary.BigEndian.Uint32(header[:])
	if size > MaxFrame {
		closeAll(files)
		return nil, fmt.Errorf("handoff: a frame of %d bytes is past the %d limit", size, MaxFrame)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(c.c, body); err != nil {
		closeAll(files)
		return nil, fmt.Errorf("handoff: read a frame: %w", err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		closeAll(files)
		return nil, fmt.Errorf("handoff: decode a frame: %w", err)
	}
	return files, nil
}

func parseRights(oob []byte) ([]*os.File, error) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("handoff: parse the descriptors: %w", err)
	}
	var files []*os.File
	for _, m := range msgs {
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			syscall.CloseOnExec(fd)
			files = append(files, os.NewFile(uintptr(fd), "handoff"))
		}
	}
	return files, nil
}

func closeAll(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// descriptors duplicates each file's descriptor for the message. os.File.Fd would hand over the
// file's own, but it first makes the file blocking, and blocking is a property of the open file
// description every duplicate shares: a socket sent that way stops honouring its deadlines on
// this side, so after a failed handoff the old daemon could never pause its listener again.
// The duplicates are the caller's to close once sent.
func descriptors(files []*os.File) ([]int, error) {
	fds := make([]int, 0, len(files))
	for _, f := range files {
		raw, err := f.SyscallConn()
		if err != nil {
			return fds, fmt.Errorf("handoff: a descriptor to send: %w", err)
		}
		var dupErr error
		if err := raw.Control(func(fd uintptr) {
			var nfd int
			nfd, dupErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
			if dupErr == nil {
				fds = append(fds, nfd)
			}
		}); err != nil {
			return fds, fmt.Errorf("handoff: a descriptor to send: %w", err)
		}
		if dupErr != nil {
			return fds, fmt.Errorf("handoff: duplicate a descriptor to send: %w", dupErr)
		}
	}
	return fds, nil
}
