//go:build unix

package pty

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

func open(t *testing.T, script string) ports.PTY {
	t.Helper()
	p, err := Open(ports.PTYSpec{
		Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"PATH=/usr/bin:/bin"},
		Size: domain.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Signal(ports.SignalKill)
		_ = p.Close()
		_, _ = p.Wait()
	})
	return p
}

// readAll reads until the PTY's end of output, failing on anything but a clean end: no
// bytes on Linux, whose EIO Read translates, and io.EOF on macOS.
func readAll(t *testing.T, p ports.PTY) []byte {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := p.Read(buf)
		out.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			return out.Bytes()
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if n == 0 {
			return out.Bytes()
		}
	}
}

// TestPauseStopsAReadAndLosesNothing_REQ_HND_001: Pause wakes a read in progress with
// ErrPaused and reads nothing while paused; after Resume, the output written meanwhile is all
// there. This is the property that lets an old daemon stop reading a PTY the new one holds.
func TestPauseStopsAReadAndLosesNothing_REQ_HND_001(t *testing.T) {
	p := open(t, "sleep 0.3; echo during-the-pause; sleep 0.3; echo after")

	woke := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := p.Read(buf)
		if n > 0 {
			err = errors.New("read " + string(buf[:n]))
		}
		woke <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := p.Pause(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-woke:
		if !errors.Is(err, ports.ErrPaused) {
			t.Fatalf("the read in progress ended with %v, want ErrPaused", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pause did not wake the read in progress")
	}

	// Paused across the first line: a read still answers ErrPaused and takes nothing.
	time.Sleep(450 * time.Millisecond)
	if n, err := p.Read(make([]byte, 64)); n != 0 || !errors.Is(err, ports.ErrPaused) {
		t.Fatalf("a read while paused got %d bytes and %v", n, err)
	}

	if err := p.Resume(); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, p)
	if !bytes.Contains(got, []byte("during-the-pause")) || !bytes.Contains(got, []byte("after")) {
		t.Fatalf("after the pause the PTY gave %q, want both lines", got)
	}
}

// TestAnAdoptedPTYReadsAndReportsAnUnknownExit_REQ_HND_001: a PTY adopted from its master and
// its child's pid reads the child's output, and Wait reports the child's end as
// ErrExitUnknown, since the code goes to its parent (delta `2026-10-pty-handoff`, decision 6).
func TestAnAdoptedPTYReadsAndReportsAnUnknownExit_REQ_HND_001(t *testing.T) {
	parent := open(t, "read line; echo got:$line; exit 7")

	// What the handoff socket would deliver: another descriptor for the same master.
	dup, err := dupFile(parent.File())
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := Adopt(dup, parent.Pid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adopted.Close() }()
	if adopted.Pid() != parent.Pid() {
		t.Fatalf("adopted pid %d, want %d", adopted.Pid(), parent.Pid())
	}

	if _, err := adopted.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, adopted); !bytes.Contains(got, []byte("got:hello")) {
		t.Fatalf("the adopted PTY read %q", got)
	}

	waited := make(chan error, 1)
	go func() {
		code, err := adopted.Wait()
		if code != -1 {
			err = errors.New("a code was reported")
		}
		waited <- err
	}()
	select {
	case err := <-waited:
		if !errors.Is(err, ports.ErrExitUnknown) {
			t.Fatalf("the adopted Wait ended with %v, want ErrExitUnknown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the adopted Wait did not see the child exit")
	}

	// The parent, which started the child, still gets its code.
	if code, err := parent.Wait(); err != nil || code != 7 {
		t.Fatalf("the parent's Wait: %d, %v", code, err)
	}
}

// TestAdoptRefusesAGonePid: a pid that is not a process cannot be adopted.
func TestAdoptRefusesAGonePid(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if _, err := Adopt(r, 0); err == nil {
		t.Fatal("pid 0 was adopted")
	}
}
