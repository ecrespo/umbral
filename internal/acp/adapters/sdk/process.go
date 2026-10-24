package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/acp-go-sdk"

	"github.com/ecrespo/umbral/internal/acp/domain"
)

// maxFrameBytes bounds one line the agent sends. A longer one is not taken for ACP: reading it
// whole would let an agent hold any amount of the daemon's memory.
const maxFrameBytes = 16 << 20

// process is one running agent: its child process, its ACP connection and its session.
type process struct {
	h      *Host
	thread string
	agent  string
	dir    string
	iso    domain.Isolation

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	conn    *acp.ClientSideConnection
	session acp.SessionId
	client  *client

	// gone closes as soon as the process is seen to exit, exited once what it left is ended
	// and it is reaped; invalid when it sent a line that is not ACP.
	gone        chan struct{}
	exited      chan struct{}
	invalid     chan struct{}
	invalidOnce sync.Once

	killOnce sync.Once
	// reaped is set, under reapMu, before the process is reaped: a signal sent to its pid or
	// group after that could reach a process that reused the pid.
	reapMu sync.Mutex
	reaped bool
	// turn serialises prompts: a thread runs one turn at a time anyway.
	turn sync.Mutex

	// terminals are the agent's terminals not released yet, by their id; opening counts the
	// ones being created, which the cap counts too (REQ-ACP-005). killing is set by a cancel's
	// sweep for the rest of the turn, so a terminal still starting then is killed as it is
	// registered (REQ-ACP-008).
	termMu    sync.Mutex
	terminals map[string]*terminal
	opening   int
	nextTerm  int
	killing   bool
}

func newProcess(h *Host, req Request, iso domain.Isolation) *process {
	p := &process{
		h: h, thread: req.ThreadID, agent: req.Agent, dir: req.Dir, iso: iso,
		gone: make(chan struct{}), exited: make(chan struct{}), invalid: make(chan struct{}), terminals: map[string]*terminal{},
	}
	p.client = &client{p: p}
	p.client.touch()
	return p
}

func (p *process) start(cmd *exec.Cmd) error {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// The agent's stderr is its own log, and it holds the env_refs' secrets: a provider's error
	// often prints the key it refused. It is not kept (Art. 7), as an MCP server's is not. nil
	// is /dev/null: a writer would be a pipe, and a child holding it would keep Wait from
	// returning after the agent itself has exited.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd, p.stdin = cmd, stdin
	pid := cmd.Process.Pid
	p.h.trackPID(pid, true)
	go func() {
		// What it started in its session ends with it, and its pid is forgotten, while the
		// agent is still an unreaped zombie: neither can then reach a process that reused the
		// pid. Where that cannot be done, the pid stays listed until kill (REQ-ORC-004, delta
		// `2026-10-hosted-agent-peer`).
		waitExit(cmd)
		close(p.gone)
		p.reapMu.Lock()
		p.reaped = true
		if forgetAtExit {
			endSession(pid)
			p.h.trackPID(pid, false)
		}
		p.reapMu.Unlock()
		if forgetAtExit {
			_ = cmd.Wait()
		}
		close(p.exited)
	}()
	r := &checkedReader{src: bufio.NewReaderSize(stdout, 64<<10), onFrame: p.client.touch, onInvalid: p.markInvalid}
	p.conn = acp.NewClientSideConnection(p.client, stdin, r)
	p.conn.SetLogger(p.h.cfg.Logger)
	return nil
}

func (p *process) markInvalid() { p.invalidOnce.Do(func() { close(p.invalid) }) }

// alive reports whether the process runs and still speaks ACP.
func (p *process) alive() bool {
	select {
	case <-p.gone:
		return false
	case <-p.invalid:
		return false
	default:
		return true
	}
}

// failure is why the process cannot go on, "" while it can.
func (p *process) failure() string {
	select {
	case <-p.invalid:
		return "it sent a message that is not ACP"
	default:
	}
	select {
	case <-p.gone:
		// Its status is known once it is reaped, which follows the cleanup of its session.
		select {
		case <-p.exited:
			if p.cmd.ProcessState != nil {
				return "its process exited: " + p.cmd.ProcessState.String()
			}
		default:
		}
		return "its process exited"
	default:
	}
	return ""
}

// call runs fn, an exchange with the agent, and ends it with the agent's error when the
// process exits or stops speaking ACP first.
func (p *process) call(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			if why := p.failure(); why != "" {
				return fmt.Errorf("%w: %s", domain.ErrAgent, why)
			}
			return fmt.Errorf("%w: %w", domain.ErrAgent, err)
		}
		return nil
	case <-p.gone:
	case <-p.invalid:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", domain.ErrAgent, ctx.Err())
	}
	return fmt.Errorf("%w: %s", domain.ErrAgent, p.failure())
}

// kill ends the process and its group, and removes its isolated home.
func (p *process) kill() {
	p.killOnce.Do(func() {
		if p.cmd != nil && p.cmd.Process != nil {
			// The group first, then the process, in case it left the group; neither once the
			// process is being reaped, when its pid may already be someone else's.
			p.reapMu.Lock()
			if !p.reaped {
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
				_ = p.cmd.Process.Kill()
			}
			p.reapMu.Unlock()
			_ = p.stdin.Close()
			select {
			case <-p.exited:
			case <-time.After(5 * time.Second):
			}
			// Forgotten here only where the exit could not forget it: elsewhere the pid may
			// already be a newer agent's, listed again.
			if !forgetAtExit {
				p.h.trackPID(p.cmd.Process.Pid, false)
			}
		}
		_ = os.RemoveAll(p.iso.Root)
		p.releaseTerminals()
	})
}

// checkedReader passes the agent's stdout to the ACP connection a line at a time, and ends it
// at the first line that is not a JSON-RPC 2.0 object (REQ-ACP-006). The SDK would log such a
// line and read on; an agent that sends one is broken, and its turn ends `agent_error`.
type checkedReader struct {
	src       *bufio.Reader
	onFrame   func()
	onInvalid func()
	pending   []byte
	err       error
}

func (r *checkedReader) Read(b []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		line, err := r.readLine()
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if !validFrame(trimmed) {
				r.onInvalid()
				r.err = io.EOF
				return 0, io.EOF
			}
			r.onFrame()
		}
		r.pending = line
		if err != nil {
			r.err = err
		}
	}
	n := copy(b, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// readLine reads up to and including a newline, refusing a line over maxFrameBytes.
func (r *checkedReader) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.src.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxFrameBytes {
			return []byte("{"), nil // not valid: ends the connection as an invalid frame
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// validFrame reports whether a line is a JSON-RPC 2.0 message: an object naming the version,
// with a method or an id.
func validFrame(line []byte) bool {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		ID      json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return false
	}
	return msg.JSONRPC == "2.0" && (msg.Method != "" || len(msg.ID) > 0)
}

// activity is an atomic clock of the agent's last message, in Unix nanoseconds.
type activity struct{ at atomic.Int64 }

func (a *activity) touch()               { a.at.Store(time.Now().UnixNano()) }
func (a *activity) since() time.Duration { return time.Since(time.Unix(0, a.at.Load())) }
