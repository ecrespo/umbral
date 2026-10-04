package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	agentsports "github.com/ecrespo/umbral/internal/agents/ports"
)

// aiDaemon is a daemon that answers `umb ai`'s calls through a script and can push
// notifications while a call is pending, which the request/response fake of main_test.go
// cannot.
type aiDaemon struct {
	mu    sync.Mutex
	calls []aiCall
	// send runs when thread.send arrives: it pushes notifications through notify and returns
	// the result or an error object.
	send func(params map[string]any, notify func(method string, params any)) (any, map[string]any)
	// frameLimit, when set, is the max_message_bytes system.hello announces.
	frameLimit int
	// sendFrames is the size of each thread.send line received, its newline included.
	sendFrames []int
}

// noReply is a thread.send result the fake never answers, as a wait still pending.
var noReply = map[string]any{"no": "reply"}

type aiCall struct {
	Method string
	Params map[string]any
}

func (d *aiDaemon) seen(method string) []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []map[string]any
	for _, c := range d.calls {
		if c.Method == method {
			out = append(out, c.Params)
		}
	}
	return out
}

func (d *aiDaemon) serve(t *testing.T) string {
	t.Helper()
	dir := socketDir(t)
	socket := filepath.Join(dir, "umbral.sock")
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("deadbeef"), 0o600); err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.conn(conn)
		}
	}()
	return socket
}

func (d *aiDaemon) conn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	rd := bufio.NewReaderSize(conn, 4<<20)
	enc := json.NewEncoder(conn)
	notify := func(method string, params any) {
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "seq": 1})
	}
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			return
		}
		d.mu.Lock()
		d.calls = append(d.calls, aiCall{Method: req.Method, Params: req.Params})
		if req.Method == "thread.send" {
			d.sendFrames = append(d.sendFrames, len(line))
		}
		d.mu.Unlock()

		var result any
		var errObj map[string]any
		switch req.Method {
		case "system.hello":
			hello := map[string]any{
				"daemon_version": "0.0.0-fake", "protocol_version": 1,
				"capabilities": []string{"threads"}, "connection_id": "con_fake",
			}
			if d.frameLimit > 0 {
				hello["max_message_bytes"] = d.frameLimit
			}
			result = hello
		case "thread.create":
			result = map[string]any{"id": "thr_mine", "mode": "ask", "ephemeral": true, "state": "idle"}
		case "thread.send":
			result, errObj = d.send(req.Params, notify)
			if r, ok := result.(map[string]any); ok && r["no"] == "reply" {
				continue
			}
		case "thread.cancel":
			result = map[string]any{"stopped_at": 5}
		default:
			errObj = map[string]any{"code": -32601, "message": "method not found", "data": map[string]any{"domain_code": "METHOD_NOT_FOUND"}}
		}
		if errObj != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": errObj})
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

func delta(thread, kind, text string) map[string]any {
	return map[string]any{"thread_id": thread, "turn_id": "trn_1", "kind": kind, "text": text}
}

func finished(reason string) map[string]any {
	return map[string]any{
		"thread_id": "thr_mine", "turn_id": "trn_1", "stop_reason": reason,
		"usage": map[string]any{"in_tokens": 10, "out_tokens": 2, "cost_micro_usd": 0},
	}
}

func answered(state string) map[string]any {
	return map[string]any{"turn_id": "trn_1", "message_id": "msg_1", "final_state": state}
}

func runAI(t *testing.T, socket, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"ai"}, args...)
	full = append(full, "--socket", socket, "--no-autostart")
	code := runIO(full, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestUmbAiPipesStdin_REQ_CLI_001 is REQ-CLI-001: `umb ai "<prompt>"` with data on stdin
// creates an ephemeral thread in `ask` mode in the shell's directory, sends the prompt with
// stdin as an attachment, and streams the response to stdout as it arrives — only the text
// of its own thread, not the model's reasoning nor another thread's deltas.
func TestUmbAiPipesStdin_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	piped := "--- FAIL: TestX (0.00s)\n    x_test.go:9: got 1, want 2\n"
	d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		notify("thread.delta", delta("thr_other", "text", "NOT MINE "))
		notify("thread.delta", delta("thr_mine", "reasoning", "thinking hard "))
		notify("thread.delta", delta("thr_mine", "text", "The test expects "))
		notify("thread.delta", delta("thr_mine", "text", "2."))
		notify("thread.turn_finished", finished("end_turn"))
		return answered("done"), nil
	}}
	socket := d.serve(t)

	code, stdout, stderr := runAI(t, socket, piped, "why", "did it fail?")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != "The test expects 2.\n" {
		t.Fatalf("stdout %q", stdout)
	}

	creates := d.seen("thread.create")
	here, _ := os.Getwd()
	if len(creates) != 1 || creates[0]["mode"] != "ask" || creates[0]["ephemeral"] != true || creates[0]["cwd"] != here {
		t.Fatalf("thread.create %+v", creates)
	}
	sends := d.seen("thread.send")
	if len(sends) != 1 {
		t.Fatalf("thread.send %+v", sends)
	}
	s := sends[0]
	if s["thread_id"] != "thr_mine" || s["text"] != "why did it fail?" {
		t.Fatalf("thread.send %+v", s)
	}
	if id, _ := s["client_msg_id"].(string); len(id) != 26 {
		t.Fatalf("client_msg_id %v is not a ULID", s["client_msg_id"])
	}
	atts, _ := s["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments %+v", s["attachments"])
	}
	a := atts[0].(map[string]any)
	data, err := base64.StdEncoding.DecodeString(fmt.Sprint(a["data_b64"]))
	if a["kind"] != "stdin" || err != nil || string(data) != piped || a["truncated"] == true {
		t.Fatalf("attachment %+v (decoded %q, %v)", a, data, err)
	}
	wait, _ := s["wait"].(map[string]any)
	until := fmt.Sprint(wait["until"])
	// Not idle: a turn nobody views ends done, and a wait for idle would then run to its
	// deadline (API §5.29).
	if until != "[done stopped blocked]" || wait["timeout_ms"] == nil {
		t.Fatalf("wait %+v", wait)
	}
}

// TestUmbAiCapsStdinAtOneMiB_REQ_CLI_001: past 1 MiB, `umb ai` sends the first 1 MiB, marks
// the attachment truncated so the model is told, says so on stderr, and still answers.
func TestUmbAiCapsStdinAtOneMiB_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	if maxStdinBytes != agentsports.MaxStdinBytes {
		t.Fatalf("umb caps stdin at %d, the daemon at %d", maxStdinBytes, agentsports.MaxStdinBytes)
	}
	d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		notify("thread.delta", delta("thr_mine", "text", "ok"))
		notify("thread.turn_finished", finished("end_turn"))
		return answered("done"), nil
	}}
	socket := d.serve(t)

	big := strings.Repeat("a", maxStdinBytes) + "TAIL-NEVER-SENT"
	code, stdout, stderr := runAI(t, socket, big, "summarize")
	if code != exitOK || stdout != "ok\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "1 MiB") {
		t.Fatalf("stderr does not note the truncation: %q", stderr)
	}
	a := d.seen("thread.send")[0]["attachments"].([]any)[0].(map[string]any)
	data, _ := base64.StdEncoding.DecodeString(fmt.Sprint(a["data_b64"]))
	if len(data) != maxStdinBytes || strings.Contains(string(data), "TAIL") || a["truncated"] != true {
		t.Fatalf("sent %d bytes, truncated=%v", len(data), a["truncated"])
	}

	// Exactly 1 MiB is not truncated.
	code, _, stderr = runAI(t, socket, strings.Repeat("b", maxStdinBytes), "summarize")
	sends := d.seen("thread.send")
	if a := sends[len(sends)-1]["attachments"].([]any)[0].(map[string]any); code != exitOK || a["truncated"] == true || strings.Contains(stderr, "1 MiB") {
		t.Fatalf("exactly 1 MiB: exit %d, truncated %v, stderr %q", code, a["truncated"], stderr)
	}
}

// TestUmbAiWithoutStdinSendsNoAttachment_REQ_CLI_001: nothing piped, nothing attached.
func TestUmbAiWithoutStdinSendsNoAttachment_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		notify("thread.turn_finished", finished("end_turn"))
		return answered("done"), nil
	}}
	socket := d.serve(t)
	if code, _, stderr := runAI(t, socket, "", "hello"); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if atts, ok := d.seen("thread.send")[0]["attachments"]; ok {
		t.Fatalf("attachments %+v with nothing on stdin", atts)
	}
}

// TestUmbAiExitsNonZeroWhenTheTurnFails_REQ_CLI_001: a turn that ends in anything but
// end_turn is an error for the script reading the exit code; what streamed stays on stdout.
func TestUmbAiExitsNonZeroWhenTheTurnFails_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"provider_error", "max_steps", "budget", "tool_error", "storage_error", "context_overflow", "cancelled"} {
		d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
			notify("thread.delta", delta("thr_mine", "text", "partial"))
			notify("thread.turn_finished", finished(reason))
			return answered("done"), nil
		}}
		socket := d.serve(t)
		code, stdout, stderr := runAI(t, socket, "x", "go")
		if code != exitFailure || !strings.Contains(stderr, reason) || stdout != "partial\n" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", reason, code, stdout, stderr)
		}
	}
}

// TestUmbAiCancelsATurnItCannotFinish_REQ_CLI_001: `umb` cannot answer an approval (API §2),
// so a turn that blocks on one is cancelled rather than left waiting for nobody; so is one
// whose wait expired. Both exit 1.
func TestUmbAiCancelsATurnItCannotFinish_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]func() (any, map[string]any){
		"blocked": func() (any, map[string]any) { return answered("blocked"), nil },
		"timeout": func() (any, map[string]any) {
			return nil, map[string]any{"code": -32011, "message": "the wait expired", "data": map[string]any{"domain_code": "TIMEOUT"}}
		},
	} {
		d := &aiDaemon{send: func(map[string]any, func(string, any)) (any, map[string]any) { return answer() }}
		socket := d.serve(t)
		code, _, stderr := runAI(t, socket, "x", "go", "--timeout", "2s")
		if code != exitFailure {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
		}
		if c := d.seen("thread.cancel"); len(c) != 1 || c[0]["thread_id"] != "thr_mine" {
			t.Errorf("%s: thread.cancel %+v", name, c)
		}
		if w := d.seen("thread.send")[0]["wait"].(map[string]any); w["timeout_ms"] != float64(2000) {
			t.Errorf("%s: --timeout 2s sent %v", name, w["timeout_ms"])
		}
	}
}

// TestUmbAiStreamsALongAnswer_REQ_CLI_001: the answer streams while thread.send's wait is
// still pending, so every delta has to be drained as it comes; more of them than the
// client's notification buffer must all reach stdout.
func TestUmbAiStreamsALongAnswer_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	const n = 5000
	d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		for i := range n {
			notify("thread.delta", delta("thr_mine", "text", fmt.Sprintf("%d,", i)))
		}
		notify("thread.turn_finished", finished("end_turn"))
		return answered("done"), nil
	}}
	socket := d.serve(t)
	code, stdout, stderr := runAI(t, socket, "", "count")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var want strings.Builder
	for i := range n {
		fmt.Fprintf(&want, "%d,", i)
	}
	if stdout != want.String()+"\n" {
		t.Fatalf("stdout has %d bytes, want %d", len(stdout), want.Len()+1)
	}
}

// TestUmbAiNeedsAPrompt_REQ_CLI_001: no prompt is exit 1 before anything is sent.
func TestUmbAiNeedsAPrompt_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	d := &aiDaemon{send: func(map[string]any, func(string, any)) (any, map[string]any) { return answered("done"), nil }}
	socket := d.serve(t)
	if code, _, stderr := runAI(t, socket, "x"); code != exitFailure || !strings.Contains(stderr, "prompt") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(d.seen("thread.create")) != 0 {
		t.Fatal("a thread was created without a prompt")
	}
}

// TestUmbAiFitsStdinToTheFrameLimit_REQ_CLI_001: at the smallest frame limit a daemon may
// announce (1 MiB, API §8), 1 MiB of stdin in base64 does not fit, and the daemon would close
// the connection over the frame. `umb ai` sends what fits, marked truncated, and says why.
func TestUmbAiFitsStdinToTheFrameLimit_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	d := &aiDaemon{frameLimit: 1 << 20, send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		notify("thread.turn_finished", finished("end_turn"))
		return answered("done"), nil
	}}
	socket := d.serve(t)
	code, _, stderr := runAI(t, socket, strings.Repeat("z", maxStdinBytes), "summarize <this> & that")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	d.mu.Lock()
	frame := d.sendFrames[0]
	d.mu.Unlock()
	if frame > 1<<20 || frame < 1<<20-1024 {
		t.Fatalf("thread.send was %d bytes against a %d limit; it must fit and not waste the room", frame, 1<<20)
	}
	a := d.seen("thread.send")[0]["attachments"].([]any)[0].(map[string]any)
	if a["truncated"] != true || !strings.Contains(stderr, "frame limit") || !strings.Contains(stderr, "umb limits set --max-message 2MiB") {
		t.Fatalf("truncated %v, stderr %q", a["truncated"], stderr)
	}
}

// blockedWriter holds every write until release is closed: a pager that is not reading.
type blockedWriter struct {
	release <-chan struct{}
	buf     bytes.Buffer
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	<-w.release
	return w.buf.Write(p)
}

// TestUmbAiOutlivesASlowStdout_REQ_CLI_001: printing must not stall draining. With stdout
// held while more deltas arrive than the client's notification buffer holds, every one of
// them still reaches stdout once it reads again.
func TestUmbAiOutlivesASlowStdout_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	const n = 5000
	release := make(chan struct{})
	d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		for i := range n {
			notify("thread.delta", delta("thr_mine", "text", "x"))
			// Another thread's events fill the buffer too.
			notify("thread.delta", delta("thr_other", "text", "y"))
			_ = i
		}
		notify("thread.turn_finished", finished("end_turn"))
		return answered("done"), nil
	}}
	socket := d.serve(t)
	go func() {
		for len(d.seen("thread.send")) == 0 {
			runtime.Gosched()
		}
		time.Sleep(300 * time.Millisecond)
		close(release)
	}()
	out := &blockedWriter{release: release}
	var stderr bytes.Buffer
	code := runIO([]string{"ai", "go", "--socket", socket, "--no-autostart"}, strings.NewReader(""), out, &stderr)
	if code != exitOK || out.buf.String() != strings.Repeat("x", n)+"\n" {
		t.Fatalf("exit %d, %d bytes out, stderr %q", code, out.buf.Len(), stderr.String())
	}
}

// TestUmbAiCancelsOnInterrupt_REQ_CLI_001: Ctrl-C while the wait is pending cancels the turn,
// which would otherwise run on for nobody, and exits 1.
func TestUmbAiCancelsOnInterrupt_REQ_CLI_001(t *testing.T) {
	t.Parallel()
	d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		notify("thread.delta", delta("thr_mine", "text", "starting"))
		return noReply, nil
	}}
	socket := d.serve(t)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		for len(d.seen("thread.send")) == 0 {
			runtime.Gosched()
		}
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	var stdout, stderr bytes.Buffer
	code := cmdAI(ctx, []string{"go", "--socket", socket, "--no-autostart"}, strings.NewReader(""), newPrinter(&stdout), newPrinter(&stderr))
	if code != exitFailure || stdout.String() != "starting\n" || !strings.Contains(stderr.String(), "interrupted") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	if c := d.seen("thread.cancel"); len(c) != 1 || c[0]["thread_id"] != "thr_mine" {
		t.Fatalf("thread.cancel %+v", c)
	}
}

// TestUmbAiDoesNotGuessAnUnseenEnd_REQ_CLI_001: the wait answered but how the turn ended
// never arrived — the bus may drop an event under pressure. An unknown end is not success.
// Not parallel: it shortens endGrace, which the parallel tests read once this one is done.
func TestUmbAiDoesNotGuessAnUnseenEnd_REQ_CLI_001(t *testing.T) {
	saved := endGrace
	endGrace = 200 * time.Millisecond
	t.Cleanup(func() { endGrace = saved })
	d := &aiDaemon{send: func(_ map[string]any, notify func(string, any)) (any, map[string]any) {
		notify("thread.delta", delta("thr_mine", "text", "an answer"))
		return answered("done"), nil
	}}
	socket := d.serve(t)
	code, stdout, stderr := runAI(t, socket, "", "go")
	if code != exitFailure || stdout != "an answer\n" || !strings.Contains(stderr, "never arrived") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
