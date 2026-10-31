package sshtunnel

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/remote/domain"
)

// buildFakeSSH builds fakessh into a temporary directory.
func buildFakeSSH(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakessh")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./fakessh").CombinedOutput()
	if err != nil {
		t.Fatalf("build fakessh: %v\n%s", err, out)
	}
	return bin
}

// echo serves a Unix socket that writes back every line.
func echo(t *testing.T, path string) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					_, _ = c.Write(append(sc.Bytes(), '\n'))
				}
			}()
		}
	}()
}

func TestTheDestinationIsNeverAnOption_REQ_RMT_001(t *testing.T) {
	target := domain.Target{User: "me", Host: "nas", Port: 2222}
	for _, args := range [][]string{
		New("", "").EndpointArgs(target),
		ForwardArgs(target, "/l.sock", "/r.sock"),
	} {
		at := slices.Index(args, "--")
		if at < 0 || args[at+1] != "me@nas" {
			t.Errorf("%q does not end ssh's options before the destination", args)
		}
		if !slices.Contains(args, "BatchMode=yes") {
			t.Errorf("%q may prompt", args)
		}
	}
	fw := ForwardArgs(target, "/l.sock", "/r.sock")
	if !slices.Contains(fw, "/l.sock:/r.sock") || !slices.Contains(fw, "-N") || !slices.Contains(fw, "StreamLocalBindMask=0177") {
		t.Errorf("the forward %q is not a private stream-local forward", fw)
	}
	for _, arg := range fw {
		if strings.HasPrefix(arg, "-D") || strings.HasPrefix(arg, "-R") || strings.Contains(arg, "localhost:") {
			t.Errorf("the forward %q opens a TCP port", fw)
		}
	}
}

func TestAnEndpointIsAskedOfTheRemoteUmb_REQ_RMT_001(t *testing.T) {
	ssh := buildFakeSSH(t)
	dir := socketDir(t)
	umb := filepath.Join(dir, "umb")
	script := "#!/bin/sh\n[ \"$1 $2\" = \"remote endpoint\" ] || exit 3\nprintf '{\"socket\":\"%s/r.sock\",\"token\":\"tok\"}\\n' \"$XDG_RUNTIME_DIR\"\n"
	if err := os.WriteFile(umb, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKESSH_ROOT", filepath.Join(dir, "hosts"))
	ep, err := New(ssh, umb).Endpoint(t.Context(), domain.Target{Host: "nas"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "hosts", "nas", "run", "r.sock"); ep.Socket != want || ep.Token != "tok" {
		t.Fatalf("Endpoint = %+v, want socket %s", ep, want)
	}

	t.Setenv("FAKESSH_REFUSE", "nas")
	_, err = New(ssh, umb).Endpoint(t.Context(), domain.Target{Host: "nas"})
	if err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("an unreachable host = %v, want ssh's reason", err)
	}
}

func TestAForwardCarriesTheSocketAndEndsWithClose_REQ_RMT_001(t *testing.T) {
	ssh := buildFakeSSH(t)
	dir := socketDir(t)
	remote, local := filepath.Join(dir, "r.sock"), filepath.Join(dir, "l.sock")
	echo(t, remote)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	fw, err := New(ssh, "").Forward(ctx, domain.Target{Host: "nas"}, local, remote)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(local)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the local socket: %v, %v", info, err)
	}
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "unix", local)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("hi\n"))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "hi\n" {
		t.Fatalf("through the forward: %q, %v", line, err)
	}
	_ = c.Close()

	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fw.Done():
	default:
		t.Fatal("Close returned before ssh ended")
	}
	if _, err := os.Stat(local); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the local socket outlived the forward: %v", err)
	}
}

func TestAForwardThatCannotStartSaysWhy_REQ_RMT_002(t *testing.T) {
	ssh := buildFakeSSH(t)
	dir := socketDir(t)
	local := filepath.Join(dir, "missing-dir", "l.sock")
	_, err := New(ssh, "").Forward(t.Context(), domain.Target{Host: "nas"}, local, filepath.Join(dir, "r.sock"))
	if err == nil || !strings.Contains(err.Error(), "missing-dir") {
		t.Fatalf("a forward that cannot bind = %v, want ssh's reason", err)
	}
}

// TestAForwardSaysNothingOfTheRemoteDaemon_REQ_RMT_002: as with OpenSSH's -L, a forward to a
// remote socket nobody listens on starts, and each connection through it ends at once. That is
// why the remote module probes the daemon before calling a host connected.
func TestAForwardSaysNothingOfTheRemoteDaemon_REQ_RMT_002(t *testing.T) {
	ssh := buildFakeSSH(t)
	dir := socketDir(t)
	local := filepath.Join(dir, "l.sock")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	fw, err := New(ssh, "").Forward(ctx, domain.Target{Host: "nas"}, local, filepath.Join(dir, "missing.sock"))
	if err != nil {
		t.Fatalf("a forward to a socket nobody listens on did not start: %v", err)
	}
	defer func() { _ = fw.Close() }()
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "unix", local)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a connection to a missing remote daemon read %v, want its end", err)
	}
}

// TestNoCallSharesAMasterOrForwardsMore_REQ_RMT_001: the user's ssh config may name a control
// master or extra forwards for the host; neither may carry a link past Close or open a TCP port.
func TestNoCallSharesAMasterOrForwardsMore_REQ_RMT_001(t *testing.T) {
	target := domain.Target{Host: "nas"}
	for _, args := range [][]string{New("", "").EndpointArgs(target), ForwardArgs(target, "/l.sock", "/r.sock")} {
		if !slices.Contains(args, "ControlMaster=no") || !slices.Contains(args, "ControlPath=none") {
			t.Errorf("%q may share a control master", args)
		}
	}
	if args := New("", "").EndpointArgs(target); !slices.Contains(args, "ClearAllForwardings=yes") {
		t.Errorf("the endpoint call %q keeps the config's forwards", args)
	}
}
