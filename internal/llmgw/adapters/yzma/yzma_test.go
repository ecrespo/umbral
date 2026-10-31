package yzma

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// fakeEngine stands in for llama.cpp: it records what it loads and opens, renders a prompt
// as role-tagged lines, counts a token per byte and generates the pieces it is given.
type fakeEngine struct {
	mu     sync.Mutex
	fail   error
	loads  []string
	opened []string
	closed []string
	pieces []string
	prompt string
}

func (e *fakeEngine) load(dir string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loads = append(e.loads, dir)
	return e.fail
}

func (e *fakeEngine) open(path string, nCtx int) (model, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opened = append(e.opened, filepath.Base(path))
	return &fakeModel{e: e, name: filepath.Base(path)}, nil
}

func (e *fakeEngine) state() (opened, closed []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.opened...), append([]string(nil), e.closed...)
}

type fakeModel struct {
	e    *fakeEngine
	name string
}

func (m *fakeModel) render(turns []turn) (string, error) {
	var b strings.Builder
	for _, t := range turns {
		b.WriteString(t.role + ": " + t.text + "\n")
	}
	m.e.mu.Lock()
	m.e.prompt = b.String()
	m.e.mu.Unlock()
	return b.String(), nil
}

func (m *fakeModel) tokenize(prompt string) ([]int32, error) { return make([]int32, len(prompt)), nil }

func (m *fakeModel) generate(ctx context.Context, _ []int32, maxTokens int, _ float32, emit func([]byte) bool) (int, error) {
	n := 0
	for _, p := range m.e.pieces {
		if n == maxTokens {
			break
		}
		if stopped(ctx) {
			// An engine may stop without saying why: the adapter reports the cancel.
			return n, nil
		}
		n++
		if !emit([]byte(p)) {
			return n, nil
		}
	}
	return n, nil
}

func stopped(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (m *fakeModel) close() {
	m.e.mu.Lock()
	m.e.closed = append(m.e.closed, m.name)
	m.e.mu.Unlock()
}

// modelsDir writes files into a new directory: each name with its contents.
func modelsDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newTest(t *testing.T, eng *fakeEngine, dir string, options map[string]any) *Provider {
	t.Helper()
	p, err := New(Config{
		ID: "embedded", LibPath: optLlama, ModelsDir: dir, Options: options,
		engine: eng, lib: &libLoader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func collect(t *testing.T, p *Provider, ctx context.Context, req domain.Request) ([]domain.Event, error) {
	t.Helper()
	seq, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	var events []domain.Event
	for ev, err := range seq {
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// TestYzmaListsTheGGUFModelsOfItsDirectory_REQ_LLM_009: a `.gguf` file whose first bytes are
// the GGUF magic is a local model with no tools and the configured window; anything else is
// skipped, and a directory that cannot be read fails discovery (delta `2026-10-yzma-provider`,
// decision 3).
func TestYzmaListsTheGGUFModelsOfItsDirectory_REQ_LLM_009(t *testing.T) {
	dir := modelsDir(t, map[string]string{
		"qwen-1.5b.gguf": "GGUF\x03\x00rest", "notes.txt": "GGUF", "broken.gguf": "PK\x03\x04",
	})
	if err := os.Mkdir(filepath.Join(dir, "sub.gguf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "qwen-1.5b.gguf"), filepath.Join(dir, "alias.gguf")); err != nil {
		t.Fatal(err)
	}
	p := newTest(t, &fakeEngine{}, dir, map[string]any{"num_ctx": int64(8192)})
	if p.ID() != "embedded" || !p.Local() {
		t.Fatalf("id %q local %v", p.ID(), p.Local())
	}
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
		if !m.Local || m.Caps.Tools || m.Caps.Vision || m.Caps.JSONSchema || m.Caps.Reasoning || m.Caps.ContextWindow != 8192 {
			t.Errorf("%s: local %v caps %+v", m.ID, m.Local, m.Caps)
		}
		if m.Provider != "embedded" || m.PriceInMicroUSDPerMTok != 0 || m.PriceOutMicroUSDPerMTok != 0 {
			t.Errorf("%s: provider %q prices %d/%d", m.ID, m.Provider, m.PriceInMicroUSDPerMTok, m.PriceOutMicroUSDPerMTok)
		}
	}
	if got := strings.Join(ids, ","); got != "embedded/alias,embedded/qwen-1.5b" {
		t.Errorf("models = %s, want embedded/alias,embedded/qwen-1.5b", got)
	}

	gone := newTest(t, &fakeEngine{}, filepath.Join(dir, "missing"), nil)
	if _, err := gone.Models(context.Background()); err == nil {
		t.Error("a missing models_dir was listed")
	}
}

// TestYzmaStreamsTextAndUsage_REQ_LLM_009: a call renders the system prompt and the messages
// through the model's template, streams each piece as text, and ends with usage at no cost and
// done; a cancel ends the stream, and what the adapter cannot carry is refused before any
// native call (decisions 4 and 5).
func TestYzmaStreamsTextAndUsage_REQ_LLM_009(t *testing.T) {
	outside := modelsDir(t, map[string]string{"outside.gguf": "GGUF"})
	dir := filepath.Join(outside, "models")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.gguf"), []byte("GGUF...."), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{pieces: []string{"Try ", "`git", " status`", "\xe2\x9c", "\x93", "\xff"}}
	p := newTest(t, eng, dir, nil)
	req := domain.Request{
		Model: "small", System: "Be brief.", MaxOutputTokens: 16,
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "gti status failed"},
			{Role: domain.RoleTool, Text: "exit 127", ToolName: "run_command"},
		},
	}
	events, err := collect(t, p, context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var usage *domain.Usage
	for i, ev := range events {
		switch ev.Kind {
		case domain.EventTextDelta:
			text.WriteString(ev.Text)
			if ev.Text == "" || !utf8.ValidString(ev.Text) {
				t.Errorf("a delta split a character: %q", ev.Text)
			}
		case domain.EventUsage:
			u := ev.Usage
			usage = &u
		case domain.EventDone:
			if i != len(events)-1 || ev.FinishReason != "stop" {
				t.Errorf("done at %d of %d, reason %q", i, len(events), ev.FinishReason)
			}
		}
	}
	if got := text.String(); got != "Try `git status`✓\uFFFD" {
		t.Errorf("text = %q", got)
	}
	if usage == nil || usage.InputTokens != int64(len(eng.prompt)) || usage.OutputTokens != 6 || usage.CostMicroUSD != 0 {
		t.Errorf("usage = %+v, want %d in, 5 out", usage, len(eng.prompt))
	}
	if want := "system: Be brief.\nuser: gti status failed\nuser: exit 127\n"; eng.prompt != want {
		t.Errorf("prompt = %q, want %q", eng.prompt, want)
	}

	// The output limit ends the answer as length.
	limited, err := collect(t, p, context.Background(), domain.Request{Model: "small", MaxOutputTokens: 2, Messages: req.Messages[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if last := limited[len(limited)-1]; last.FinishReason != "length" {
		t.Errorf("a limited answer ended %q, want length", last.FinishReason)
	}

	// A cancel ends the stream with the context's error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collect(t, p, ctx, req); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled call: %v, want context.Canceled", err)
	}

	// A prompt that leaves no room for the answer in the window is refused, permanently.
	small := newTest(t, &fakeEngine{}, dir, map[string]any{"num_ctx": int64(512)})
	long := domain.Request{Model: "small", MaxOutputTokens: 100, Messages: []domain.Message{{Role: domain.RoleUser, Text: strings.Repeat("x", 500)}}}
	var perr *domain.ProviderError
	if _, err := collect(t, small, context.Background(), long); !errors.As(err, &perr) || !perr.Permanent {
		t.Errorf("a prompt over the window: %v, want a permanent provider error", err)
	}

	// What the adapter cannot carry is refused before anything is loaded.
	refuse := &fakeEngine{}
	q := newTest(t, refuse, dir, nil)
	for name, r := range map[string]domain.Request{
		"tools":     {Model: "small", Tools: []domain.ToolSpec{{Name: "run_command"}}},
		"schema":    {Model: "small", ResponseSchema: map[string]any{"type": "object"}},
		"reasoning": {Model: "small", Reasoning: "high"},
		"vision":    {Model: "small", Vision: true},
	} {
		if _, err := q.Stream(context.Background(), r); !errors.Is(err, domain.ErrUnsupported) {
			t.Errorf("%s: %v, want ErrUnsupported", name, err)
		}
	}
	for _, name := range []string{"../outside", "missing", "sub/small", ".hidden", ""} {
		if _, err := collect(t, q, context.Background(), domain.Request{Model: name, Messages: req.Messages[:1]}); err == nil {
			t.Errorf("model %q was served", name)
		}
	}
	if opened, _ := refuse.state(); len(opened) != 0 || len(refuse.loads) != 0 {
		t.Errorf("a refused call loaded %v, opened %v", refuse.loads, opened)
	}
}

// TestYzmaKeepsOneModelAndReleasesIt_REQ_LLM_009: a model is loaded on its first call and
// kept; a call for another frees it first, and keep_alive frees it after a quiet spell
// (decision 6).
func TestYzmaKeepsOneModelAndReleasesIt_REQ_LLM_009(t *testing.T) {
	dir := modelsDir(t, map[string]string{"a.gguf": "GGUF", "b.gguf": "GGUF"})
	eng := &fakeEngine{pieces: []string{"ok"}}
	p := newTest(t, eng, dir, map[string]any{"keep_alive": 0.2})
	call := func(name string) {
		t.Helper()
		if _, err := collect(t, p, context.Background(), domain.Request{Model: name, Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}); err != nil {
			t.Fatal(err)
		}
	}
	call("a")
	call("a")
	call("b")
	opened, closed := eng.state()
	if strings.Join(opened, ",") != "a.gguf,b.gguf" || strings.Join(closed, ",") != "a.gguf" {
		t.Fatalf("opened %v closed %v, want a then b with a freed", opened, closed)
	}
	if len(eng.loads) != 1 || eng.loads[0] != optLlama {
		t.Errorf("libraries loaded %v, want once from /opt/llama", eng.loads)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, closed := eng.state(); len(closed) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("keep_alive never freed the model")
		}
		time.Sleep(20 * time.Millisecond)
	}
	call("b")
	if opened, _ := eng.state(); len(opened) != 3 {
		t.Errorf("a freed model was not loaded again: opened %v", opened)
	}
}

// TestYzmaRefusesASecondLibPath_REQ_LLM_009: llama.cpp is loaded once per process, so a
// provider naming other libraries than the ones loaded is refused, and so is a configuration
// the adapter cannot run (decision 2).
func TestYzmaRefusesASecondLibPath_REQ_LLM_009(t *testing.T) {
	dir := modelsDir(t, map[string]string{"a.gguf": "GGUF"})
	lib := &libLoader{}
	eng := &fakeEngine{pieces: []string{"ok"}}
	first, err := New(Config{ID: "one", LibPath: optLlama, ModelsDir: dir, engine: eng, lib: lib})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, first, context.Background(), domain.Request{Model: "a", Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{ID: "two", LibPath: optOther, ModelsDir: dir, engine: eng, lib: lib}); err == nil {
		t.Error("a second lib_path was accepted after the first was loaded")
	}
	if _, err := New(Config{ID: "three", LibPath: optLlama, ModelsDir: dir, engine: eng, lib: lib}); err != nil {
		t.Errorf("the same lib_path: %v", err)
	}

	// Two providers built before any load: the second one's call is refused, permanently.
	early := &libLoader{}
	a, errA := New(Config{ID: "a", LibPath: optLlama, ModelsDir: dir, engine: eng, lib: early})
	b, errB := New(Config{ID: "b", LibPath: optOther, ModelsDir: dir, engine: eng, lib: early})
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	hi := domain.Request{Model: "a", Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	if _, err := collect(t, a, context.Background(), hi); err != nil {
		t.Fatal(err)
	}
	var perr *domain.ProviderError
	if _, err := collect(t, b, context.Background(), hi); !errors.As(err, &perr) || !perr.Permanent {
		t.Errorf("a call loading a second lib_path: %v, want a permanent provider error", err)
	}

	// A load that failed pins its directory too: it may have left libraries in the process.
	half := &libLoader{}
	broken := &fakeEngine{fail: errors.New("libllama: not found")}
	c, err := New(Config{ID: "c", LibPath: optLlama, ModelsDir: dir, engine: broken, lib: half})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, c, context.Background(), hi); err == nil {
		t.Fatal("a failed load served a call")
	}
	if _, err := New(Config{ID: "d", LibPath: optOther, ModelsDir: dir, engine: eng, lib: half}); err == nil {
		t.Error("another lib_path was accepted after a load from /opt/llama failed")
	}

	for name, cfg := range map[string]Config{
		"no id":           {LibPath: optLlama, ModelsDir: dir},
		"relative lib":    {ID: "x", LibPath: "lib", ModelsDir: dir},
		"no models_dir":   {ID: "x", LibPath: optLlama},
		"relative models": {ID: "x", LibPath: optLlama, ModelsDir: "models"},
		"small window":    {ID: "x", LibPath: optLlama, ModelsDir: dir, Options: map[string]any{"num_ctx": int64(100)}},
		"string window":   {ID: "x", LibPath: optLlama, ModelsDir: dir, Options: map[string]any{"num_ctx": "4096"}},
		"hot":             {ID: "x", LibPath: optLlama, ModelsDir: dir, Options: map[string]any{"temperature": 3.0}},
		"negative keep":   {ID: "x", LibPath: optLlama, ModelsDir: dir, Options: map[string]any{"keep_alive": int64(-1)}},
		"unknown option":  {ID: "x", LibPath: optLlama, ModelsDir: dir, Options: map[string]any{"num_gpu": int64(1)}},
	} {
		cfg.engine, cfg.lib = eng, &libLoader{}
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
