package api

import (
	"encoding/json"
	"testing"
	"time"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

func wire(t *testing.T, method string, params any) map[string]any {
	t.Helper()

	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("%s does not marshal: %v", method, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("%s does not round-trip: %v", method, err)
	}
	return decoded
}

func TestBlockNotificationsMatchTheSchema_REQ_BLK_001_REQ_BLK_002(t *testing.T) {
	startedAt := time.UnixMilli(1_757_592_001_000).UTC()
	endedAt := startedAt.Add(4200 * time.Millisecond)
	exitCode := 1

	open := sessdomain.Block{
		ID: "blk_1", SessionID: "ses_1", Origin: sessdomain.OriginUser,
		Command: "go test ./...", CWD: "/home/u/repo", Host: "thinkpad",
		State: sessdomain.BlockRunning, StartedAt: startedAt,
	}

	method, params := toNotification(sessports.BlockStarted{Block: open})
	if method != "block.started" {
		t.Fatalf("method is %q, want block.started", method)
	}
	payload := wire(t, method, params)

	// API Spec §4 lists every key of Block. A client that reads one the daemon never
	// sends, or that gets a running block already carrying an end time, is a client
	// written against a contract the daemon does not keep.
	for _, key := range []string{
		"id", "session_id", "origin", "thread_id", "command", "cwd", "host", "state",
		"exit_code", "started_at", "ended_at", "duration_ms", "output_bytes",
		"output_truncated",
	} {
		if _, ok := payload[key]; !ok {
			t.Errorf("block.started has no %q", key)
		}
	}
	switch {
	case payload["state"] != "running":
		t.Errorf("state is %v, want running", payload["state"])
	case payload["thread_id"] != nil:
		t.Errorf("thread_id is %v, want null for a user block", payload["thread_id"])
	case payload["ended_at"] != nil:
		t.Errorf("ended_at is %v, want null while the block is open", payload["ended_at"])
	case payload["duration_ms"] != nil:
		t.Errorf("duration_ms is %v, want null while the block is open", payload["duration_ms"])
	case payload["started_at"] != float64(startedAt.UnixMilli()):
		t.Errorf("started_at is %v, want epoch milliseconds", payload["started_at"])
	}

	done := open
	done.State = sessdomain.BlockFinished
	done.ExitCode = &exitCode
	done.EndedAt = &endedAt
	done.OutputBytes = 1834

	method, params = toNotification(sessports.BlockClosed{Block: done})
	if method != "block.closed" {
		t.Fatalf("method is %q, want block.closed", method)
	}
	payload = wire(t, method, params)
	switch {
	case payload["exit_code"] != float64(1):
		t.Errorf("exit_code is %v, want 1", payload["exit_code"])
	case payload["duration_ms"] != float64(4200):
		t.Errorf("duration_ms is %v, want 4200", payload["duration_ms"])
	case payload["output_bytes"] != float64(1834):
		t.Errorf("output_bytes is %v, want 1834", payload["output_bytes"])
	case payload["output_truncated"] != false:
		t.Errorf("output_truncated is %v, want false", payload["output_truncated"])
	}

	method, params = toNotification(sessports.BlockUpdated{
		BlockID: "blk_1", State: sessdomain.BlockInteractive,
	})
	if method != "block.updated" {
		t.Fatalf("method is %q, want block.updated", method)
	}
	payload = wire(t, method, params)
	if payload["block_id"] != "blk_1" || payload["state"] != "interactive" {
		t.Errorf("block.updated is %v, want the id and the new state", payload)
	}
}

func TestSessionIntegrationNotification_REQ_BLK_003(t *testing.T) {
	method, params := toNotification(sessports.SessionIntegration{
		SessionID: "ses_1", Integration: sessdomain.IntegrationNone,
	})
	if method != "session.integration" {
		t.Fatalf("method is %q, want session.integration", method)
	}
	payload := wire(t, method, params)
	if payload["session_id"] != "ses_1" || payload["integration"] != "none" {
		t.Errorf("payload is %v, want the session and its integration", payload)
	}
}
