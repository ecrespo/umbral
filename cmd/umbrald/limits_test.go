package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// rawPeer is a JSON-RPC peer on a bare socket, for what `internal/client` will not do on
// purpose: send a line past the limit, or a hello too large to be one.
type rawPeer struct {
	t    *testing.T
	conn net.Conn
	dec  *json.Decoder
}

type rawReply struct {
	ID     json.RawMessage `json:"id"`
	Result map[string]any  `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func dialRaw(ctx context.Context, t *testing.T, socket string) *rawPeer {
	t.Helper()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		t.Fatalf("dial %s: %v", socket, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &rawPeer{t: t, conn: conn, dec: json.NewDecoder(conn)}
}

// padded is one request of exactly size bytes, `\n` included: the JSON, then spaces.
func padded(t *testing.T, id int, method string, params any, size int) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	if len(body)+1 > size {
		t.Fatalf("%d bytes cannot hold %s", size, body)
	}
	return append(append(body, bytes.Repeat([]byte(" "), size-len(body)-1)...), '\n')
}

// send writes a line. A daemon refusing it may hang up before the tail is written, which is
// the refusal arriving early (EPIPE, ECONNRESET) and not a failure: the reply is there to read.
func (p *rawPeer) send(line []byte) {
	p.t.Helper()
	_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := p.conn.Write(line); err != nil &&
		!errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
		p.t.Fatalf("write: %v", err)
	}
}

func (p *rawPeer) read() rawReply {
	p.t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var r rawReply
	if err := p.dec.Decode(&r); err != nil {
		p.t.Fatalf("read: %v", err)
	}
	return r
}

func (p *rawPeer) expectClosed() {
	p.t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var discard json.RawMessage
	err := p.dec.Decode(&discard)
	closed := errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET)
	if !closed {
		p.t.Fatalf("the connection stayed open (%v, %s)", err, discard)
	}
}

func helloParams(token string) map[string]any {
	return map[string]any{
		"token": token, "client_kind": "cli", "client_version": "test", "protocol_version": 1,
	}
}

// greet completes the handshake and returns the limit the daemon announced.
func (p *rawPeer) greet(token string) int64 {
	p.t.Helper()
	line, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "system.hello", "params": helloParams(token),
	})
	p.send(append(line, '\n'))
	r := p.read()
	if r.Error != nil {
		p.t.Fatalf("hello refused: %d", r.Error.Code)
	}
	limit, _ := r.Result["max_message_bytes"].(float64)
	return int64(limit)
}

// TestLimitsSetRaisesTheLimitForNewConnections_REQ_CLI_007 runs `limits.set` against a real
// daemon — every `XDG_*` directory redirected, `XDG_CONFIG_HOME` included, so the settings
// file it rewrites is the test's (AGENTS.md) — and checks each clause of the requirement and
// each row of the delta's file-cases table through the daemon rather than beside it.
func TestLimitsSetRaisesTheLimitForNewConnections_REQ_CLI_007(t *testing.T) {
	bin := buildDaemon(t)
	runtime, daemonDir := isolatedRuntime(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	settings := filepath.Join(configDir, "config.toml")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const original = "# mine\n[experimental]\npane_history = false  # keep\n\n" +
		"[api]\nmax_message_bytes = \"4MiB\"   # the default, for now\n"
	writeFile(t, settings, original)

	stop := startStoppableDaemon(t, bin, runtime)
	socket := filepath.Join(daemonDir, "umbral.sock")
	token := strings.TrimSpace(readFile(t, filepath.Join(daemonDir, "token")))

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	c, err := client.Connect(ctx, client.Options{SocketPath: socket, NoAutostart: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	set := func(n int) error {
		var out map[string]any
		return c.Call(ctx, "limits.set", map[string]any{"max_message_bytes": n}, &out)
	}
	const fourMiB, fiveMiB, eightMiB = 4 << 20, 5 << 20, 8 << 20

	// A connection greeted before the change, kept open across it.
	old := dialRaw(ctx, t, socket)
	if got := old.greet(token); got != fourMiB {
		t.Fatalf("greeted with %d before any change, want %d", got, fourMiB)
	}

	if err := set(eightMiB); err != nil {
		t.Fatalf("limits.set 8 MiB: %v", err)
	}

	t.Run("the one line is rewritten and every other byte kept", func(t *testing.T) {
		want := strings.Replace(original, `"4MiB"`, "8388608", 1)
		if got := readFile(t, settings); got != want {
			t.Errorf("config.toml =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("a new connection is greeted with 8 MiB and can use it", func(t *testing.T) {
		fresh := dialRaw(ctx, t, socket)
		if got := fresh.greet(token); got != eightMiB {
			t.Errorf("greeted with %d, want %d", got, eightMiB)
		}
		fresh.send(padded(t, 2, "system.status", nil, fiveMiB))
		if r := fresh.read(); r.Error != nil {
			t.Errorf("a 5 MiB request under an 8 MiB limit = error %d", r.Error.Code)
		}
		reconnected, err := client.Connect(ctx, client.Options{SocketPath: socket, NoAutostart: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reconnected.Close() }()
		if reconnected.MaxMessageBytes != eightMiB {
			t.Errorf("internal/client reads with %d, want the announced %d",
				reconnected.MaxMessageBytes, eightMiB)
		}
	})

	t.Run("an old connection keeps 4 MiB", func(t *testing.T) {
		old.send(padded(t, 2, "system.status", nil, fiveMiB))
		if r := old.read(); r.Error == nil || r.Error.Code != -32602 {
			t.Fatalf("a 5 MiB request on a connection greeted at 4 MiB = %+v, want VALIDATION_ERROR", r)
		}
		old.expectClosed()
	})

	t.Run("before the handshake the limit is still 4 MiB", func(t *testing.T) {
		early := dialRaw(ctx, t, socket)
		early.send(padded(t, 1, "system.hello", helloParams(token), fiveMiB))
		r := early.read()
		if r.Error == nil || r.Error.Code != -32001 || string(r.ID) != "null" {
			t.Fatalf("a 5 MiB hello = %+v, want UNAUTHORIZED with a null id", r)
		}
		early.expectClosed()
	})

	t.Run("an invalid size is refused and the file is untouched", func(t *testing.T) {
		before := readFile(t, settings)
		for _, n := range []int{65 << 20, 1<<20 - 1, 0} {
			if err := set(n); client.DomainCode(err) != "VALIDATION_ERROR" {
				t.Errorf("limits.set %d = %v, want VALIDATION_ERROR", n, err)
			}
		}
		if got := readFile(t, settings); got != before {
			t.Errorf("a refused size changed the file to\n%s", got)
		}
	})

	t.Run("file cases", func(t *testing.T) {
		t.Run("absent: created with [api] and the key only", func(t *testing.T) {
			if err := os.Remove(settings); err != nil {
				t.Fatal(err)
			}
			if err := set(eightMiB); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, settings); got != "[api]\nmax_message_bytes = 8388608\n" {
				t.Errorf("created %q", got)
			}
		})

		for name, body := range map[string]string{
			"does not parse":  "[api\nmax_message_bytes = 1\n",
			"a dotted key":    "api.max_message_bytes = 4194304\n",
			"an inline table": "api = { max_message_bytes = 4194304 }\n",
		} {
			t.Run(name+": CONFIG_INVALID, untouched", func(t *testing.T) {
				writeFile(t, settings, body)
				err := set(eightMiB)
				if client.DomainCode(err) != "CONFIG_INVALID" {
					t.Errorf("limits.set = %v, want CONFIG_INVALID", err)
				}
				if name != "does not parse" && (err == nil || !strings.Contains(err.Error(), "line 1")) {
					t.Errorf("the refusal %v does not name the line", err)
				}
				if got := readFile(t, settings); got != body {
					t.Errorf("the file became %q", got)
				}
			})
		}

		t.Run("a trailing comment is kept", func(t *testing.T) {
			writeFile(t, settings, "[api]\nmax_message_bytes = 4194304  # why\n")
			if err := set(eightMiB); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, settings); got != "[api]\nmax_message_bytes = 8388608  # why\n" {
				t.Errorf("rewrote to %q", got)
			}
		})

		t.Run("a symlink survives and its target is rewritten", func(t *testing.T) {
			// Beside the configuration directory rather than in this subtest's TempDir, which
			// is gone when it ends: the restart below reads through this link.
			target := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dotfiles", "umbral.toml")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, target, "[api]\nmax_message_bytes = 4194304\n")
			if err := os.Remove(settings); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, settings); err != nil {
				t.Fatal(err)
			}
			if err := set(eightMiB); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(settings); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("config.toml is no longer a symlink (%v)", err)
			}
			if got := readFile(t, target); got != "[api]\nmax_message_bytes = 8388608\n" {
				t.Errorf("the target is %q", got)
			}
		})
	})

	// Persisted means a restarted daemon greets with it: the file is what `limits.set`
	// wrote, and the daemon reads it at start. Not a subtest: the restart belongs to this
	// test's daemon, and its cleanup to this test.
	_ = c.Close()
	stop(os.Interrupt)
	startStoppableDaemon(t, bin, runtime)
	after := dialRaw(ctx, t, socket)
	if got := after.greet(strings.TrimSpace(readFile(t, filepath.Join(daemonDir, "token")))); got != eightMiB {
		t.Errorf("greeted with %d after a restart, want the persisted %d", got, eightMiB)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- the test's own files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
