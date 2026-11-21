package config

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// closeAgain is how long a pipe listener's Close waits for go-winio's accept loop to end
// before it signals it again.
const closeAgain = 50 * time.Millisecond

// ListenPipe listens on the named pipe name with go-winio, under security descriptor sddl
// ("" is the default one), and returns a listener whose Close returns.
//
// go-winio v0.6.2's own Close can wait for ever (T-HRD-23). It hands its accept loop one
// signal and waits for the loop to end. If that signal arrives while the loop is waiting for
// a client, and a client that connected and hung up at once makes the wait end with
// ERROR_NO_DATA instead, the loop takes the error for "try again" and waits for the next
// client — with the signal spent, and nobody left to send another. The loop does read a new
// signal on every such wait, so Close here sends one again until the loop has ended.
func ListenPipe(name, sddl string) (net.Listener, error) {
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    PipeBufferSize,
		OutputBufferSize:   PipeBufferSize,
	})
	if err != nil {
		return nil, err
	}
	return &pipeListener{Listener: ln}, nil
}

type pipeListener struct{ net.Listener }

// Close ends the listener and returns once its accept loop has. Every signal it sends is a
// call of go-winio's Close, each of which returns when the loop ends, so none is left behind.
func (l *pipeListener) Close() error {
	done := make(chan struct{})
	go func() {
		_ = l.Listener.Close()
		close(done)
	}()
	for {
		select {
		case <-done:
			return nil
		case <-time.After(closeAgain):
			go func() { _ = l.Listener.Close() }()
		}
	}
}
