package activeai

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/activeai/ports"
	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

// fakeModel answers every call with its answer, after its delay, and records what it was asked.
type fakeModel struct {
	mu     sync.Mutex
	asked  []string
	answer string
	err    error
	delay  time.Duration
}

func (m *fakeModel) Suggest(ctx context.Context, system, user string) (string, string, error) {
	m.mu.Lock()
	m.asked = append(m.asked, user)
	m.mu.Unlock()
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	return m.answer, "embedded/small", m.err
}

func (m *fakeModel) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.asked...)
}

type fakeBlocks struct{ output string }

func (b fakeBlocks) Output(context.Context, string, string) (string, error) { return b.output, nil }

func failed(id string, exit int) sessports.BlockClosed {
	end := time.Now()
	return sessports.BlockClosed{Block: sessdomain.Block{
		ID: id, SessionID: "ses_1", Origin: sessdomain.OriginUser, Command: "gti status",
		CWD: "/home/me/repo", State: sessdomain.BlockFinished, ExitCode: &exit, EndedAt: &end,
	}}
}

// run starts a service on a bus and returns the bus and a subscription to its suggestions.
func run(t *testing.T, model ports.Model, perMinute int, now func() time.Time) (*bus.Bus, *bus.Subscription) {
	t.Helper()
	b := bus.New()
	sub := b.Subscribe(ports.KindBlockSuggestion)
	svc, err := New(Config{Enabled: true, PerMinute: perMinute, Now: now}, b, model,
		fakeBlocks{output: "line one\nzsh: command not found: gti\n"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; sub.Close(); b.Close() })
	return b, sub
}

func next(t *testing.T, sub *bus.Subscription, wait time.Duration) (ports.BlockSuggestion, bool) {
	t.Helper()
	select {
	case ev := <-sub.C():
		return ev.(ports.BlockSuggestion), true
	case <-time.After(wait):
		return ports.BlockSuggestion{}, false
	}
}

// TestAFailedBlockGetsASuggestion_REQ_AAI_001: a user's command that ended with a non-zero
// exit is sent to the model with its command, exit code, directory and output, and the first
// line of the answer is published as block.suggestion; a success, an interrupt and an agent's
// command are not (delta `2026-10-active-ai`, decisions 1 to 3).
func TestAFailedBlockGetsASuggestion_REQ_AAI_001(t *testing.T) {
	t.Parallel()
	model := &fakeModel{answer: "\n`git status`\nIt was a typo.\n"}
	b, sub := run(t, model, 4, time.Now)

	ok := failed("blk_ok", 0)
	interrupted := failed("blk_int", 130)
	agent := failed("blk_agent", 1)
	agent.Block.Origin = sessdomain.OriginAgent
	for _, ev := range []sessports.BlockClosed{ok, interrupted, agent} {
		b.Publish(ev)
	}
	b.Publish(failed("blk_1", 127))

	got, seen := next(t, sub, 2*time.Second)
	if !seen {
		t.Fatal("no suggestion")
	}
	if got.BlockID != "blk_1" || got.SessionID != "ses_1" || got.Text != "git status" || got.Model != "embedded/small" {
		t.Errorf("suggestion %+v", got)
	}
	asked := model.calls()
	if len(asked) != 1 {
		t.Fatalf("the model was asked %d times, want once: %q", len(asked), asked)
	}
	for _, want := range []string{"gti status", "127", "/home/me/repo", "command not found: gti"} {
		if !strings.Contains(asked[0], want) {
			t.Errorf("the prompt lacks %q: %q", want, asked[0])
		}
	}

	// A disabled service asks nothing.
	off := bus.New()
	defer off.Close()
	svc, err := New(Config{Enabled: false, PerMinute: 4}, off, model, fakeBlocks{})
	if err != nil || svc != nil {
		t.Errorf("a disabled service: %v, %v; want none", svc, err)
	}
}

// TestSuggestionsAreRateLimited_REQ_AAI_001: no more than per_minute suggestions are asked in
// a sliding minute, a failure while one is being made is dropped, and neither drop uses up the
// window (decision 5).
func TestSuggestionsAreRateLimited_REQ_AAI_001(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	clock := time.Unix(1_800_000_000, 0)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }

	model := &fakeModel{answer: "git status"}
	b, sub := run(t, model, 2, now)
	for i, id := range []string{"blk_1", "blk_2", "blk_3"} {
		b.Publish(failed(id, 1))
		if _, seen := next(t, sub, time.Second); seen != (i < 2) {
			t.Fatalf("failure %d: suggested %v, want %v", i+1, seen, i < 2)
		}
	}
	advance(61 * time.Second)
	b.Publish(failed("blk_4", 1))
	if _, seen := next(t, sub, time.Second); !seen {
		t.Fatal("a failure after the window was not suggested")
	}

	// One at a time: a failure while the model is busy is dropped and not counted.
	advance(61 * time.Second)
	model.mu.Lock()
	model.delay = 300 * time.Millisecond
	model.mu.Unlock()
	b.Publish(failed("blk_5", 1))
	time.Sleep(50 * time.Millisecond)
	b.Publish(failed("blk_6", 1))
	if got, seen := next(t, sub, 2*time.Second); !seen || got.BlockID != "blk_5" {
		t.Fatalf("busy: %+v %v, want blk_5", got, seen)
	}
	if _, seen := next(t, sub, 500*time.Millisecond); seen {
		t.Error("a failure that arrived while busy was suggested too")
	}
	model.mu.Lock()
	model.delay = 0
	model.mu.Unlock()
	b.Publish(failed("blk_7", 1))
	if _, seen := next(t, sub, time.Second); !seen {
		t.Error("the dropped failure used up the window")
	}
}

// TestNoLocalModelNoSuggestion_REQ_AAI_003: when the model port finds no local candidate, or
// answers nothing, no suggestion is published (decisions 3 and 4).
func TestNoLocalModelNoSuggestion_REQ_AAI_003(t *testing.T) {
	t.Parallel()
	for name, model := range map[string]*fakeModel{
		"no local model": {err: errors.New("no candidate")},
		"empty answer":   {answer: " \n\n"},
	} {
		b, sub := run(t, model, 4, time.Now)
		b.Publish(failed("blk_1", 2))
		if got, seen := next(t, sub, 500*time.Millisecond); seen {
			t.Errorf("%s: suggested %+v", name, got)
		}
		if len(model.calls()) != 1 {
			t.Errorf("%s: asked %d times", name, len(model.calls()))
		}
	}
}
