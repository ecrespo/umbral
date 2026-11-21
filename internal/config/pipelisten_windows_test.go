package config

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// TestAPipeListenersCloseReturns_REQ_WIN_001: closing the daemon's pipe returns, whatever its
// clients are doing. go-winio v0.6.2's Close waits for ever when it meets an accept whose
// client connected and hung up at once (T-HRD-23): the daemon would never stop, and a test's
// fake daemon held its test for the suite's ten minutes. Each round races a close against
// clients that connect and hang up as fast as they can.
func TestAPipeListenersCloseReturns_REQ_WIN_001(t *testing.T) {
	const (
		rounds  = 300
		dialers = 4
		within  = 10 * time.Second
	)
	dir := t.TempDir()
	for round := range rounds {
		name := PipeName(filepath.Join(dir, fmt.Sprintf("r%d", round), SocketFileName))
		ln, err := ListenPipe(name, "")
		if err != nil {
			t.Fatal(err)
		}
		ctx, stop := context.WithCancel(t.Context())
		var clients sync.WaitGroup
		connected := make(chan struct{}, 1)
		for range dialers {
			clients.Go(func() {
				for ctx.Err() == nil {
					dial, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
					c, err := winio.DialPipeContext(dial, name)
					cancel()
					if err != nil {
						continue
					}
					_ = c.Close()
					select {
					case connected <- struct{}{}:
					default:
					}
				}
			})
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		// The close comes once clients are connecting, a little later each round so that it
		// lands at every point of an accept.
		select {
		case <-connected:
		case <-time.After(within):
			stop()
			t.Fatalf("round %d: no client connected", round)
		}
		time.Sleep(time.Duration(round%20) * 50 * time.Microsecond)
		closed := make(chan struct{})
		go func() {
			_ = ln.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(within):
			stop()
			t.Fatalf("round %d: the listener's Close did not return in %s", round, within)
		}
		stop()
		clients.Wait()
	}
}
