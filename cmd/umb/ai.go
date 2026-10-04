package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/ecrespo/umbral/internal/client"
)

// maxStdinBytes is REQ-CLI-001's 1 MiB: how much of stdin `umb ai` reads and sends. It is
// the daemon's agents/ports.MaxStdinBytes, spelled out here so `umb` does not link the agent
// runtime's types; a test holds the two equal.
const maxStdinBytes = 1 << 20

// defaultAITimeout bounds a turn `umb ai` waits for. API §8 allows a wait of 1 s to 1 h.
const defaultAITimeout = 10 * time.Minute

// endGrace is how long `umb ai` waits for thread.turn_finished once thread.send's wait has
// answered: the two leave the daemon in either order (API §5.20, §6). A variable so a test
// need not wait it out.
var endGrace = 5 * time.Second

// cancelTimeout bounds the thread.cancel `umb ai` sends for a turn it gives up on. A cancel
// returns within 500 ms (REQ-AGT-007); the rest is margin.
const cancelTimeout = 3 * time.Second

// cmdAI is `umb ai "<prompt>"` (REQ-CLI-001): an ephemeral thread in `ask` mode in the
// shell's directory, stdin as its attachment when something was piped, the answer streamed
// to stdout. It exits 0 when the turn ends with end_turn and 1 otherwise.
func cmdAI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr *printer) int {
	fs := flag.NewFlagSet("umb ai", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	fs.StringVar(&f.socket, "socket", "", "path to the daemon socket")
	fs.StringVar(&f.daemonPath, "daemon-path", "", "path to the umbrald binary to autostart")
	fs.BoolVar(&f.noAutostart, "no-autostart", false, "do not start umbrald if it is not running")
	model := fs.String("model", "", "the model to use, e.g. ollama/gpt-oss:20b; default is the code class")
	timeout := fs.Duration("timeout", defaultAITimeout, "how long to wait for the answer, 1s to 1h")
	pos, rest, err := parseInterleaved(fs, args)
	if err != nil {
		return exitFailure
	}
	prompt := strings.TrimSpace(strings.Join(append(pos, rest...), " "))
	if prompt == "" {
		stderr.println(`umb ai: expected a prompt, e.g. go test ./... 2>&1 | umb ai "why does it fail?"`)
		return exitFailure
	}
	if *timeout < time.Second || *timeout > time.Hour {
		stderr.println("umb ai: --timeout must be between 1s and 1h")
		return exitFailure
	}
	cwd, err := os.Getwd()
	if err != nil {
		stderr.printf("umb ai: the current directory: %v\n", err)
		return exitFailure
	}

	// Read before connecting: a pipe whose writer is still running is read to its end or to
	// the limit first, so the turn does not start on half of it. A pipe that never closes is
	// waited on like any Unix filter would; --timeout bounds the turn, not the input.
	data, truncated, err := readStdin(stdin)
	if err != nil {
		stderr.printf("umb ai: read stdin: %v\n", err)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	frameLimit := c.MaxMessageBytes
	s := client.NewStream(c)
	defer func() { _ = s.Close() }()

	create := map[string]any{"mode": "ask", "cwd": cwd, "ephemeral": true, "title": "umb ai"}
	if *model != "" {
		create["model"] = *model
	}
	var thread struct {
		ID string `json:"id"`
	}
	createCtx, cancel := context.WithTimeout(ctx, callTimeout)
	err = s.Call(createCtx, "thread.create", create, &thread)
	cancel()
	if err != nil {
		return callFailed(stderr, "", err)
	}

	send := map[string]any{
		"thread_id":     thread.ID,
		"text":          prompt,
		"client_msg_id": ulid.MustNew(ulid.Now(), rand.Reader).String(),
		// A wait observes attention (API §5.29): a turn nobody views ends `done`, and one a
		// client happened to view ends `idle`, which answers at once as the state it ended in.
		// `idle` is not a target: after `done` only a client viewing the thread reaches it, so a
		// wait for it would run to its deadline.
		"wait": map[string]any{"until": []string{"done", "stopped", "blocked"}, "timeout_ms": timeout.Milliseconds()},
	}
	if data != nil {
		send["attachments"] = []any{stdinAttachment(send, data, truncated, frameLimit, stderr)}
	}
	return streamTurn(ctx, s, thread.ID, send, *timeout, stdout, stderr)
}

// readStdin reads at most maxStdinBytes of stdin, returning nil when nothing was piped, and
// whether more followed. A terminal on stdin is never read: the user has nothing to send and
// would otherwise have to type ^D.
func readStdin(stdin io.Reader) ([]byte, bool, error) {
	if f, ok := stdin.(*os.File); ok {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return nil, false, nil //nolint:nilerr // a stdin that cannot be examined has nothing to send
		}
	}
	// One byte past the limit says whether more followed, without reading a stream that
	// may never end.
	data, err := io.ReadAll(io.LimitReader(stdin, maxStdinBytes+1))
	if err != nil || len(data) == 0 {
		return nil, false, err
	}
	if len(data) > maxStdinBytes {
		return data[:maxStdinBytes], true, nil
	}
	return data, false, nil
}

// frameMargin covers what the request's encoding adds to the params measured here: the
// envelope's id, which grows with the call count.
const frameMargin = 64

// stdinAttachment is the `stdin` attachment for send, cut to fit the frame limit the daemon
// announced for this connection: at the 1 MiB minimum (API §8), 1 MiB of stdin in base64 is
// a frame the daemon refuses and closes the connection over. What is cut is marked truncated,
// so the model is told, and the user is told why on stderr.
func stdinAttachment(send map[string]any, data []byte, truncated bool, frameLimit int, stderr *printer) map[string]any {
	a := map[string]any{"kind": "stdin", "data_b64": "", "truncated": true}
	send["attachments"] = []any{a}
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "thread.send", "params": send})
	if err == nil && frameLimit > 0 {
		room := max(0, (frameLimit-len(frame)-1-frameMargin)/4*3)
		if len(data) > room {
			data, truncated = data[:room], true
			stderr.printf("umb ai: stdin is larger than the daemon's %s frame limit allows; only its first %d bytes are attached.\n",
				formatSize(int64(frameLimit)), room)
			if next := suggestLimit(int64(frameLimit) + 1); next > 0 {
				stderr.printf("     Raise it with: umb limits set --max-message %dMiB\n", next>>20)
			}
		}
	}
	if truncated && len(data) == maxStdinBytes {
		stderr.println("umb ai: stdin is over 1 MiB; only its first 1 MiB is attached")
	}
	a["data_b64"] = base64.StdEncoding.EncodeToString(data)
	if !truncated {
		delete(a, "truncated")
	}
	return a
}

type sendAnswer struct {
	FinalState string `json:"final_state"`
}

type turnEnd struct {
	StopReason string `json:"stop_reason"`
}

// relay drains the stream as fast as it delivers and holds this thread's events, unbounded,
// for a reader that may be slower: printing waits on stdout, and a pager that is not reading
// would otherwise fill the stream's bounded buffer, which ends the stream rather than block
// (client.ErrStreamOverflow). Every connection receives every thread's events (API §6), so
// the others are dropped here. The answer's length bounds what is held.
func relay(in <-chan client.Notification, threadID string, done <-chan struct{}) <-chan client.Notification {
	out := make(chan client.Notification)
	go func() {
		defer close(out)
		var queue []client.Notification
		for in != nil || len(queue) > 0 {
			var send chan client.Notification
			var head client.Notification
			if len(queue) > 0 {
				send, head = out, queue[0]
			}
			select {
			case n, open := <-in:
				if !open {
					in = nil
					continue
				}
				var ev struct {
					ThreadID string `json:"thread_id"`
				}
				if (n.Method == "thread.delta" || n.Method == "thread.turn_finished") && n.Decode(&ev) == nil && ev.ThreadID == threadID {
					queue = append(queue, n)
				}
			case send <- head:
				queue = queue[1:]
			case <-done:
				return
			}
		}
	}()
	return out
}

// streamTurn sends the message and prints the thread's text deltas while the send's wait is
// pending, until the turn's end is seen. The deltas are drained as they come: the stream's
// buffer is bounded, and an answer longer than it must not end the stream.
func streamTurn(ctx context.Context, s *client.Stream, threadID string, send map[string]any,
	timeout time.Duration, stdout, stderr *printer,
) int {
	replies := make(chan error, 1)
	var answer sendAnswer
	// The wait's own timeout answers first; this bound only covers a daemon that never does.
	// Cancelled on return too: a pending call holds the connection, which Close then waits for.
	callCtx, cancelCall := context.WithTimeout(ctx, timeout+callTimeout)
	defer cancelCall()
	go func() { replies <- s.Call(callCtx, "thread.send", send, &answer) }()

	done := make(chan struct{})
	defer close(done)
	events := relay(s.Notifications(), threadID, done)

	w := &textWriter{out: stdout}
	var grace <-chan time.Time
	// Ctrl-C ends the call's context too, and its reply takes the cancel path below.
	interrupted := false
	stop := ctx.Done()
	for {
		select {
		case n, open := <-events:
			if !open {
				w.finish()
				stderr.printf("umb ai: the connection to the daemon ended: %v\n", s.Err())
				return exitFailure
			}
			switch n.Method {
			case "thread.delta":
				var d struct {
					Kind string `json:"kind"`
					Text string `json:"text"`
				}
				if n.Decode(&d) == nil && d.Kind == "text" {
					w.write(d.Text)
				}
			case "thread.turn_finished":
				var end turnEnd
				if n.Decode(&end) != nil {
					continue
				}
				w.finish()
				if end.StopReason != "end_turn" {
					stderr.printf("umb ai: the turn ended with %s\n", end.StopReason)
					return exitFailure
				}
				return exitOK
			}
		case err := <-replies:
			replies = nil
			switch {
			case err != nil && (interrupted || errors.Is(ctx.Err(), context.Canceled)):
				w.finish()
				cancelTurn(ctx, s, threadID, stderr)
				stderr.println("umb ai: interrupted; the turn was cancelled")
				return exitFailure
			case client.DomainCode(err) == "TIMEOUT":
				w.finish()
				cancelTurn(ctx, s, threadID, stderr)
				stderr.printf("umb ai: no answer within %s; the turn was cancelled\n", timeout)
				return exitFailure
			case err != nil:
				w.finish()
				return callFailed(stderr, "", err)
			case answer.FinalState == "blocked":
				// `umb` cannot answer an approval (API §2), so nothing would ever resume it.
				w.finish()
				cancelTurn(ctx, s, threadID, stderr)
				stderr.println("umb ai: the turn asked for an approval, which umb cannot give; it was cancelled")
				return exitFailure
			}
			grace = time.After(endGrace)
		case <-grace:
			w.finish()
			stderr.println("umb ai: the turn ended, but how it ended never arrived")
			return exitFailure
		case <-stop:
			// Ctrl-C while the wait is pending. The reply is on its way, cancelled with the
			// command's context; stop selecting here so that it is read.
			interrupted, stop = true, nil
		}
	}
}

// cancelTurn stops the thread's turn: a turn `umb ai` gave up on is not left running for
// nobody. It outlives the command's context, which Ctrl-C may already have ended, under a
// short deadline of its own.
func cancelTurn(ctx context.Context, s *client.Stream, threadID string, stderr *printer) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()
	if err := s.Call(ctx, "thread.cancel", map[string]any{"thread_id": threadID}, nil); err != nil {
		stderr.printf("umb ai: cancel the turn: %s\n", describeError(err))
	}
}

// textWriter prints the answer as it streams and ends it with a newline, so the shell's
// prompt does not land on the answer's last line.
type textWriter struct {
	out  *printer
	last byte
}

func (w *textWriter) write(s string) {
	if s == "" {
		return
	}
	w.out.print(s)
	w.last = s[len(s)-1]
}

func (w *textWriter) finish() {
	if w.last != 0 && w.last != '\n' {
		w.out.print("\n")
	}
	w.last = '\n'
}
