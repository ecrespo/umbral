// Package yzma is the embedded provider (REQ-LLM-009): a GGUF model loaded into the daemon
// through llama.cpp's shared libraries, with no server and no network (delta
// `2026-10-yzma-provider`). It serves text only and is meant for the `fast` class.
package yzma

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
)

// The options models.toml may give a yzma provider, with their defaults and bounds.
const (
	DefaultNumCtx      = 4096
	MinNumCtx          = 512
	MaxNumCtx          = 32768
	DefaultTemperature = 0.2
	MaxTemperature     = 2.0
	DefaultMaxOutput   = 256
	DefaultKeepAlive   = 300 * time.Second
)

const (
	optNumCtx      = "num_ctx"
	optTemperature = "temperature"
	optKeepAlive   = "keep_alive"
	ggufMagic      = "GGUF"
	ggufSuffix     = ".gguf"
)

// Config is one `type = "yzma"` provider of models.toml.
type Config struct {
	ID string
	// LibPath is the absolute directory holding llama.cpp's shared libraries.
	LibPath string
	// ModelsDir is the absolute directory whose `<name>.gguf` files are the models.
	ModelsDir string
	Options   map[string]any

	// engine and lib are llama.cpp and the process's one load of it unless a test says
	// otherwise.
	engine engine
	lib    *libLoader
}

// engine is llama.cpp as the adapter uses it.
type engine interface {
	// load loads the shared libraries in dir; it is called once per process.
	load(dir string) error
	// open loads a GGUF file with a context of nCtx tokens.
	open(path string, nCtx int) (model, error)
}

// model is one loaded GGUF file. It is used by one call at a time.
type model interface {
	// render applies the model's own chat template, leaving the assistant's turn open.
	render(turns []turn) (string, error)
	tokenize(prompt string) ([]int32, error)
	// generate decodes prompt and samples up to maxTokens tokens, handing emit each piece; it
	// stops when emit returns false or ctx is done, and returns how many tokens it sampled.
	generate(ctx context.Context, prompt []int32, maxTokens int, temperature float32, emit func([]byte) bool) (int, error)
	close()
}

// turn is one message for the chat template.
type turn struct{ role, text string }

// libLoader is the process's one load of llama.cpp: purego cannot unload it, so the first
// directory a load was tried from is the only one (decision 2). A load that failed half way
// may have left some of its libraries in the process, so only that directory may try again.
type libLoader struct {
	mu     sync.Mutex
	path   string
	loaded bool
}

// process is the libLoader every real provider shares.
var process = &libLoader{}

// conflict is the directory already loaded when it is not dir.
func (l *libLoader) conflict(dir string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path != "" && l.path != dir {
		return l.path
	}
	return ""
}

func (l *libLoader) load(e engine, dir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.path != "" && l.path != dir:
		return fmt.Errorf("llama.cpp was loaded from %s; a daemon loads one lib_path", l.path)
	case l.loaded:
		return nil
	}
	l.path = dir
	if err := e.load(dir); err != nil {
		return fmt.Errorf("loading llama.cpp from %s: %w", dir, err)
	}
	l.loaded = true
	return nil
}

// Provider is a yzma provider. One call runs at a time and one model is resident.
type Provider struct {
	cfg         Config
	numCtx      int
	temperature float32
	keepAlive   time.Duration

	mu sync.Mutex
	// current is the resident model and name its file's name; used counts calls, so a
	// keep-alive timer armed before a later call frees nothing.
	current model
	name    string
	used    uint64
	timer   *time.Timer
}

// New builds the provider. It reads nothing from disk and loads nothing.
func New(cfg Config) (*Provider, error) {
	switch {
	case cfg.ID == "":
		return nil, errors.New("yzma: a provider id is required")
	case !filepath.IsAbs(cfg.LibPath):
		return nil, fmt.Errorf("yzma: provider %s: lib_path must be an absolute directory", cfg.ID)
	case !filepath.IsAbs(cfg.ModelsDir):
		return nil, fmt.Errorf("yzma: provider %s: models_dir must be an absolute directory", cfg.ID)
	}
	if cfg.engine == nil {
		cfg.engine = llamaEngine{}
	}
	if cfg.lib == nil {
		cfg.lib = process
	}
	if loaded := cfg.lib.conflict(cfg.LibPath); loaded != "" {
		return nil, fmt.Errorf("yzma: provider %s: llama.cpp is already loaded from %s, not %s", cfg.ID, loaded, cfg.LibPath)
	}
	p := &Provider{cfg: cfg, numCtx: DefaultNumCtx, temperature: DefaultTemperature, keepAlive: DefaultKeepAlive}
	for key, v := range cfg.Options {
		n, ok := number(v)
		switch {
		case key == optNumCtx && (!ok || n != float64(int(n)) || n < MinNumCtx || n > MaxNumCtx):
			return nil, fmt.Errorf("yzma: provider %s: options.num_ctx must be an integer from %d to %d", cfg.ID, MinNumCtx, MaxNumCtx)
		case key == optNumCtx:
			p.numCtx = int(n)
		case key == optTemperature && (!ok || n < 0 || n > MaxTemperature):
			return nil, fmt.Errorf("yzma: provider %s: options.temperature must be from 0 to %g", cfg.ID, MaxTemperature)
		case key == optTemperature:
			p.temperature = float32(n)
		case key == optKeepAlive && (!ok || n < 0):
			return nil, fmt.Errorf("yzma: provider %s: options.keep_alive must be seconds, 0 or more", cfg.ID)
		case key == optKeepAlive:
			p.keepAlive = time.Duration(n * float64(time.Second))
		default:
			return nil, fmt.Errorf("yzma: provider %s: options.%s is not an option of yzma", cfg.ID, key)
		}
	}
	return p, nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// Format keeps a provider's configuration out of a log line.
func (p *Provider) Format(f fmt.State, _ rune) { _, _ = fmt.Fprintf(f, "yzma provider %q", p.cfg.ID) }

// ID implements ports.Provider.
func (p *Provider) ID() string { return p.cfg.ID }

// Local implements ports.Provider: the model runs inside the daemon.
func (p *Provider) Local() bool { return true }

// Models implements ports.Provider: every `<name>.gguf` file of models_dir, or symlink to
// one, that starts with the GGUF magic (decision 3). It loads nothing.
func (p *Provider) Models(ctx context.Context) ([]domain.Model, error) {
	entries, err := os.ReadDir(p.cfg.ModelsDir)
	if err != nil {
		return nil, fmt.Errorf("yzma: provider %s: %w", p.cfg.ID, err)
	}
	var models []domain.Model
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ggufSuffix)
		if !ok || name == "" || !isGGUF(filepath.Join(p.cfg.ModelsDir, e.Name())) {
			continue
		}
		models = append(models, domain.Model{
			ID: domain.QualifiedID(p.cfg.ID, name), Provider: p.cfg.ID, Local: true,
			Caps: domain.Capabilities{ContextWindow: int64(p.numCtx)},
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, ctx.Err()
}

// isGGUF reports whether path is a regular file, or leads to one, starting with the magic:
// llama.cpp is never handed bytes that are not GGUF.
func isGGUF(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path) //nolint:gosec // models_dir is the user's own directory
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, len(ggufMagic))
	if _, err := io.ReadFull(f, magic); err != nil {
		return false
	}
	return string(magic) == ggufMagic
}

// Stream implements ports.Provider. What the adapter cannot carry is refused here; the model
// is loaded and run as the stream is read (decisions 4 and 5).
func (p *Provider) Stream(ctx context.Context, req domain.Request) (iter.Seq2[domain.Event, error], error) {
	switch {
	case len(req.Tools) > 0, req.ResponseSchema != nil, req.Reasoning != "", req.Vision:
		return nil, &domain.ProviderError{Provider: p.cfg.ID, Err: fmt.Errorf("%w: yzma serves text only", domain.ErrUnsupported)}
	}
	turns := make([]turn, 0, len(req.Messages)+1)
	if req.System != "" {
		turns = append(turns, turn{role: "system", text: req.System})
	}
	for _, m := range req.Messages {
		role := string(m.Role)
		if m.Role == domain.RoleTool {
			// The template has no tool turns the model was trained on: a result is user text.
			role = string(domain.RoleUser)
		}
		turns = append(turns, turn{role: role, text: m.Text})
	}
	maxOut := DefaultMaxOutput
	if req.MaxOutputTokens > 0 {
		maxOut = int(min(req.MaxOutputTokens, int64(p.numCtx)))
	}
	return func(yield func(domain.Event, error) bool) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.used++
		if p.timer != nil {
			p.timer.Stop()
		}
		defer p.arm()
		in, out, err := p.run(ctx, req.Model, turns, maxOut, yield)
		switch {
		case err != nil:
			yield(domain.Event{}, err)
		case out < 0:
			// The reader stopped.
		default:
			if !yield(domain.Event{Kind: domain.EventUsage, Usage: domain.Usage{InputTokens: int64(in), OutputTokens: int64(out)}}, nil) {
				return
			}
			reason := "stop"
			if out >= maxOut {
				reason = "length"
			}
			yield(domain.Event{Kind: domain.EventDone, FinishReason: reason}, nil)
		}
	}, nil
}

// run makes one call with p.mu held: the prompt's and the answer's token counts, or an out of
// -1 when the reader stopped reading.
func (p *Provider) run(ctx context.Context, name string, turns []turn, maxOut int, yield func(domain.Event, error) bool) (int, int, error) {
	m, err := p.model(name)
	if err != nil {
		return 0, 0, err
	}
	prompt, err := m.render(turns)
	if err != nil {
		return 0, 0, p.permanent(fmt.Errorf("the chat template: %w", err))
	}
	tokens, err := m.tokenize(prompt)
	if err != nil {
		return 0, 0, p.permanent(err)
	}
	if len(tokens)+maxOut > p.numCtx {
		return 0, 0, p.permanent(fmt.Errorf("the prompt takes %d tokens and the answer up to %d, over the window of %d (options.num_ctx)",
			len(tokens), maxOut, p.numCtx))
	}
	var pending []byte
	stopped := false
	n, err := m.generate(ctx, tokens, maxOut, p.temperature, func(piece []byte) bool {
		pending = append(pending, piece...)
		text := completeRunes(pending)
		if text == 0 {
			return true
		}
		// A byte-fallback token can be invalid on its own: it is never sent as text.
		chunk := string(bytes.ToValidUTF8(pending[:text], []byte("\uFFFD")))
		pending = append(pending[:0], pending[text:]...)
		if !yield(domain.Event{Kind: domain.EventTextDelta, Text: chunk}, nil) {
			stopped = true
			return false
		}
		return true
	})
	switch {
	case stopped:
		return len(tokens), -1, nil
	case err != nil:
		return len(tokens), n, err
	case ctx.Err() != nil:
		return len(tokens), n, ctx.Err()
	}
	if len(pending) > 0 {
		// A model may end inside a character; what is left is shown as the replacement rune.
		if !yield(domain.Event{Kind: domain.EventTextDelta, Text: string(bytes.ToValidUTF8(pending, []byte("�")))}, nil) {
			return len(tokens), -1, nil
		}
	}
	return len(tokens), n, nil
}

// completeRunes is how many leading bytes of b are whole characters: a token may end in the
// middle of one, and a text delta never does.
func completeRunes(b []byte) int {
	end := len(b)
	for back := 1; back < utf8.UTFMax && back <= len(b); back++ {
		if r := b[len(b)-back]; utf8.RuneStart(r) {
			if !utf8.FullRune(b[len(b)-back:]) {
				end = len(b) - back
			}
			break
		}
	}
	return end
}

// model returns the resident model for name, loading the libraries and the file as needed
// and freeing another model first (decision 6).
func (p *Provider) model(name string) (model, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return nil, p.permanent(fmt.Errorf("%q is not a model of models_dir", name))
	}
	if p.current != nil && p.name == name {
		return p.current, nil
	}
	path := filepath.Join(p.cfg.ModelsDir, name+ggufSuffix)
	if !isGGUF(path) {
		return nil, p.permanent(fmt.Errorf("%s is not a GGUF file in models_dir", name+ggufSuffix))
	}
	if err := p.cfg.lib.load(p.cfg.engine, p.cfg.LibPath); err != nil {
		return nil, p.permanent(err)
	}
	p.release()
	m, err := p.cfg.engine.open(path, p.numCtx)
	if err != nil {
		return nil, p.permanent(fmt.Errorf("loading %s: %w", name+ggufSuffix, err))
	}
	p.current, p.name = m, name
	return m, nil
}

// release frees the resident model; p.mu is held.
func (p *Provider) release() {
	if p.current != nil {
		p.current.close()
		p.current, p.name = nil, ""
	}
}

// arm starts the keep-alive timer after a call; p.mu is held. A keep_alive of 0 keeps the
// model.
func (p *Provider) arm() {
	if p.keepAlive == 0 || p.current == nil {
		return
	}
	call := p.used
	p.timer = time.AfterFunc(p.keepAlive, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.used == call {
			p.release()
		}
	})
}

// Close frees the resident model.
func (p *Provider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
	}
	p.release()
}

// permanent is a failure no retry or other candidate of this provider avoids.
func (p *Provider) permanent(err error) error {
	return &domain.ProviderError{Provider: p.cfg.ID, Err: err, Permanent: true}
}
