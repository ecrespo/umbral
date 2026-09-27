package threadstore

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/agents/domain"
	"github.com/ecrespo/umbral/internal/agents/ports"
	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/store"
)

func open(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func thread(t *testing.T, s *Store, budget int64) domain.Thread {
	t.Helper()
	th := domain.Thread{
		ID: store.NewID(store.PrefixThread), Mode: secdomain.ModeNormal, ModelClass: "code", Cwd: "/w",
		State: domain.StateIdle, AttentionState: "idle", MaxSteps: 50, BudgetTokens: budget, CreatedAt: 1, UpdatedAt: 1,
	}
	if err := s.CreateThread(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	return th
}

func userMessage(th domain.Thread, clientID string) domain.Message {
	return domain.Message{
		ID: store.NewID(store.PrefixMessage), ThreadID: th.ID, TurnID: "trn_1", Role: domain.RoleUser,
		Content: "hi", ClientMsgID: clientID, CreatedAt: 2,
		Attachments: []domain.Attachment{{Kind: "file", Ref: "a.go", Bytes: 10}},
	}
}

func TestBeginTurnPersistsAndGuards_REQ_AGT_015(t *testing.T) {
	s := open(t)
	th := thread(t, s, 1000)
	ctx := t.Context()
	first := userMessage(th, "01J9Z3K8T2QH6W4V5X7Y8Z9A0B")
	_, running, err := s.BeginTurn(ctx, first, 3)
	if err != nil || running.State != domain.StateRunning || running.Mode != secdomain.ModeNormal {
		t.Fatalf("BeginTurn returns the thread it read: %+v %v", running, err)
	}
	got, _ := s.Thread(ctx, th.ID)
	if got.State != domain.StateRunning || got.AttentionState != "working" {
		t.Fatalf("thread after BeginTurn: %+v", got)
	}
	// The same client id, even while running, returns the original.
	orig, _, err := s.BeginTurn(ctx, userMessage(th, first.ClientMsgID), 4)
	if !errors.Is(err, ports.ErrDuplicate) || orig.ID != first.ID || orig.TurnID != "trn_1" {
		t.Fatalf("duplicate: %+v %v", orig, err)
	}
	// Another message while running conflicts.
	if _, _, err := s.BeginTurn(ctx, userMessage(th, ""), 4); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("while running: %v", err)
	}
	if err := s.FinishTurn(ctx, th.ID, domain.StateIdle, domain.Usage{InTokens: 900, OutTokens: 100, CostMicroUSD: 42}, 5); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Thread(ctx, th.ID)
	if got.State != domain.StateIdle || got.TokensUsed != 1000 || got.CostMicroUSD != 42 || got.AttentionState != "done" {
		t.Fatalf("thread after the turn: %+v", got)
	}
	if _, _, err := s.BeginTurn(ctx, userMessage(th, ""), 6); !errors.Is(err, domain.ErrBudgetExceeded) {
		t.Fatalf("past the budget: %v", err)
	}
	if _, _, err := s.BeginTurn(ctx, domain.Message{ID: store.NewID(store.PrefixMessage), ThreadID: "thr_nope", TurnID: "t", Role: domain.RoleUser}, 7); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
	msgs, _ := s.Messages(ctx, th.ID)
	if len(msgs) != 1 || msgs[0].Attachments[0].Ref != "a.go" || msgs[0].ClientMsgID != first.ClientMsgID {
		t.Fatalf("messages %+v", msgs)
	}
}

func TestMessagesAndToolCallsRoundTrip_REQ_AGT_011(t *testing.T) {
	s := open(t)
	th := thread(t, s, 1000)
	ctx := t.Context()
	am := domain.Message{ID: store.NewID(store.PrefixMessage), ThreadID: th.ID, TurnID: "trn_1", Role: domain.RoleAssistant, Content: "hel", CreatedAt: 2}
	if err := s.AppendMessage(ctx, am); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendContent(ctx, am.ID, "lo"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendContent(ctx, "msg_missing", "x"); err == nil {
		t.Fatal("appending to a missing message must fail")
	}
	c := domain.ToolCall{
		ID: store.NewID(store.PrefixToolCall), ThreadID: th.ID, MessageID: am.ID, Tool: "fetch_url",
		Risk: "Network", Args: json.RawMessage(`{"url":"https://x"}`), Status: domain.ToolPending, StartedAt: 3,
	}
	if err := s.SaveToolCall(ctx, c); err != nil {
		t.Fatal(err)
	}
	end := int64(4)
	c.Status, c.Result, c.ResultSummary, c.Tainted, c.EndedAt = domain.ToolOK, "<html>", "200 OK", true, &end
	if err := s.SaveToolCall(ctx, c); err != nil {
		t.Fatal(err)
	}
	later := domain.Message{ID: store.NewID(store.PrefixMessage), ThreadID: th.ID, TurnID: "trn_1", Role: domain.RoleUser, Content: "next", CreatedAt: 2}
	if err := s.AppendMessage(ctx, later); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Messages(ctx, th.ID)
	calls, _ := s.ToolCalls(ctx, th.ID)
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "next" {
		t.Fatalf("messages %+v", msgs)
	}
	if len(calls) != 1 || calls[0].Status != domain.ToolOK || calls[0].Result != "<html>" || !calls[0].Tainted ||
		calls[0].ResultSummary != "200 OK" || *calls[0].EndedAt != 4 || string(calls[0].Args) != `{"url":"https://x"}` {
		t.Fatalf("calls %+v", calls)
	}
}

func TestUpdateThreadRefusesAModeChangeDuringATurn_REQ_AGT_010(t *testing.T) {
	s := open(t)
	th := thread(t, s, 1000)
	ctx := t.Context()
	model, title := "ollama/b", "renamed"
	got, err := s.UpdateThread(ctx, th.ID, domain.UpdateParams{Model: &model, Title: &title}, 9)
	if err != nil || got.Model != model || got.Title != title || got.UpdatedAt != 9 {
		t.Fatalf("update %+v %v", got, err)
	}
	if _, _, err := s.BeginTurn(ctx, userMessage(th, ""), 10); err != nil {
		t.Fatal(err)
	}
	ask := secdomain.ModeAsk
	if _, err := s.UpdateThread(ctx, th.ID, domain.UpdateParams{Mode: &ask}, 11); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mode during a turn: %v", err)
	}
	if _, err := s.UpdateThread(ctx, th.ID, domain.UpdateParams{Model: &model}, 11); err != nil {
		t.Fatalf("a model change during a turn is allowed (it applies next turn): %v", err)
	}
	if _, err := s.UpdateThread(ctx, "thr_nope", domain.UpdateParams{Mode: &ask}, 11); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

func TestThreadsListLeavesOutEphemeral_REQ_AGT_001(t *testing.T) {
	s := open(t)
	kept := thread(t, s, 10)
	eph := domain.Thread{
		ID: store.NewID(store.PrefixThread), Mode: secdomain.ModeAsk, ModelClass: "code", Cwd: "/w",
		State: domain.StateIdle, AttentionState: "idle", Ephemeral: true, MaxSteps: 5, BudgetTokens: 10, CreatedAt: 1, UpdatedAt: 1,
	}
	if err := s.CreateThread(t.Context(), eph); err != nil {
		t.Fatal(err)
	}
	list, err := s.Threads(t.Context())
	if err != nil || len(list) != 1 || list[0].ID != kept.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := s.Thread(t.Context(), "thr_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestRulesAreTheThreadsAndTheGlobalOnes_REQ_AGT_014(t *testing.T) {
	s := open(t)
	a, b := thread(t, s, 10), thread(t, s, 10)
	for _, row := range []struct {
		thread any
		tool   string
	}{{nil, "run_command"}, {a.ID, "write_file"}, {b.ID, "fetch_url"}} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO policy_rules(thread_id, tool, pattern, decision, source, created_at)
			VALUES (?, ?, 'go test*', 'allow', 'user_decision', 1)`, row.thread, row.tool); err != nil {
			t.Fatal(err)
		}
	}
	rules, err := s.Rules(t.Context(), a.ID)
	if err != nil || len(rules) != 2 || rules[0].Tool != "run_command" || rules[0].ThreadID != "" ||
		rules[1].ThreadID != a.ID || rules[1].Decision != secdomain.VerdictAllow || rules[1].Pattern != "go test*" {
		t.Fatalf("rules %+v %v", rules, err)
	}
}
