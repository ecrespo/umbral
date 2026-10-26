package main

import (
	"context"
	"encoding/base64"
	"iter"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/llmgw"
	llmdomain "github.com/ecrespo/umbral/internal/llmgw/domain"
)

// TestADaemonSuggestsOnAFailedBlock_REQ_AAI_001: with `[active_ai] enabled`, a user's command
// that fails in a real daemon's pane is sent to the local `fast` model with its command and
// exit code, and its answer's first line reaches the client as block.suggestion; a command
// that succeeds asks nothing (delta `2026-10-active-ai`).
func TestADaemonSuggestsOnAFailedBlock_REQ_AAI_001(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"git status\nIt was a typo."},"done":true,"done_reason":"stop","prompt_eval_count":30,"eval_count":4}`},
	}}
	srv := ollama.server(t)
	stream, ctx, _ := startAgentDaemon(t, srv.URL, daemonOpts{settings: "[active_ai]\nenabled = true\n"})

	var session struct{ ID string }
	if err := stream.Call(ctx, "session.create", map[string]any{"cols": 100, "rows": 30, "cwd": t.TempDir()}, &session); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"true", "gti status"} {
		if err := stream.Call(ctx, "session.input", map[string]any{"session_id": session.ID, "data_b64": base64.StdEncoding.EncodeToString([]byte(line + "\n"))}, nil); err != nil {
			t.Fatal(err)
		}
	}

	var failed string
	for {
		select {
		case n, ok := <-stream.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", stream.Err())
			}
			switch n.Method {
			case "block.closed":
				var b struct {
					ID       string `json:"id"`
					Command  string `json:"command"`
					ExitCode *int   `json:"exit_code"`
				}
				if n.Decode(&b) == nil && b.Command == "gti status" {
					failed = b.ID
				}
			case "block.suggestion":
				var s struct {
					BlockID   string `json:"block_id"`
					SessionID string `json:"session_id"`
					Text      string `json:"text"`
					Model     string `json:"model"`
				}
				if err := n.Decode(&s); err != nil {
					t.Fatal(err)
				}
				if failed == "" || s.BlockID != failed || s.SessionID != session.ID || s.Text != "git status" || s.Model != "ollama/m" {
					t.Fatalf("block.suggestion %+v, want git status for %s", s, failed)
				}
				ollama.mu.Lock()
				bodies := append([]string(nil), ollama.bodies...)
				ollama.mu.Unlock()
				if len(bodies) != 1 || !strings.Contains(bodies[0], "gti status") || !strings.Contains(bodies[0], "Exit code: 127") {
					t.Fatalf("the model was asked %d times: %q", len(bodies), bodies)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("no block.suggestion")
		}
	}
}

// recordingRouter answers every call with one line and records the call.
type recordingRouter struct{ calls []llmgw.Call }

func (r *recordingRouter) Stream(_ context.Context, call llmgw.Call) (iter.Seq2[llmdomain.Event, error], error) {
	r.calls = append(r.calls, call)
	return func(yield func(llmdomain.Event, error) bool) {
		if yield(llmdomain.Event{Kind: llmdomain.EventTextDelta, Text: "git status"}, nil) {
			yield(llmdomain.Event{Kind: llmdomain.EventUsage, Usage: llmdomain.Usage{Model: "ollama/m"}}, nil)
		}
	}, nil
}

// TestActiveAIAsksTheRouterLocalOnly_REQ_AAI_003: Active AI's model call is the `fast` class with
// LocalOnly set, which the router honours by dropping every remote candidate
// (TestALocalOnlyCallNeverReachesARemoteProvider_REQ_AAI_003), with no tools.
func TestActiveAIAsksTheRouterLocalOnly_REQ_AAI_003(t *testing.T) {
	t.Parallel()
	r := &recordingRouter{}
	text, model, err := localSuggester{router: r}.Suggest(t.Context(), "system", "Command: gti status")
	if err != nil || text != "git status" || model != "ollama/m" {
		t.Fatalf("Suggest = %q, %q, %v", text, model, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("%d calls", len(r.calls))
	}
	c := r.calls[0]
	if !c.LocalOnly || c.Class != suggestionClass || c.Model != "" || len(c.Request.Tools) != 0 {
		t.Errorf("call = %+v, want the fast class, local only, no model and no tools", c)
	}
}
