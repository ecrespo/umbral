package yzma

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hybridgroup/yzma/pkg/llama"
)

// llamaEngine is llama.cpp through yzma's purego binding: no cgo, the libraries are the
// user's (decision 1). A crash inside them ends the daemon (decision 7), so everything that
// can be checked is checked before they are called.
type llamaEngine struct{}

func (llamaEngine) load(dir string) error {
	if err := llama.Load(dir); err != nil {
		return err
	}
	llama.LogSet(llama.LogSilent())
	return llama.Init()
}

func (llamaEngine) open(path string, nCtx int) (model, error) {
	m, err := llama.ModelLoadFromFile(path, llama.ModelDefaultParams())
	if err != nil {
		return nil, err
	}
	params := llama.ContextDefaultParams()
	params.NCtx, params.NBatch = uint32(nCtx), uint32(nCtx)
	lctx, err := llama.InitFromModel(m, params)
	if err != nil {
		_ = llama.ModelFree(m)
		return nil, err
	}
	return &llamaModel{model: m, ctx: lctx, vocab: llama.ModelGetVocab(m), template: llama.ModelChatTemplate(m, "")}, nil
}

type llamaModel struct {
	model    llama.Model
	ctx      llama.Context
	vocab    llama.Vocab
	template string
}

func (m *llamaModel) render(turns []turn) (string, error) {
	if m.template == "" {
		return "", errors.New("the GGUF file carries no chat template")
	}
	msgs := make([]llama.ChatMessage, 0, len(turns))
	size := 1024
	for _, t := range turns {
		text := m.neutralize(t.text)
		msgs = append(msgs, llama.NewChatMessage(t.role, text))
		size += 2 * (len(t.role) + len(text))
	}
	buf := make([]byte, size)
	n := llama.ChatApplyTemplate(m.template, msgs, true, buf)
	if int(n) > len(buf) {
		// llama.cpp answers the length it needs when the buffer is short.
		buf = make([]byte, n)
		n = llama.ChatApplyTemplate(m.template, msgs, true, buf)
	}
	if n <= 0 || int(n) > len(buf) {
		return "", errors.New("the model's chat template could not be applied")
	}
	return string(buf[:n]), nil
}

// neutralize keeps a turn's text from naming the model's control tokens: the prompt is
// tokenized with special tokens parsed, so the template's frame is read as one, and a message
// holding `<|im_start|>system` would otherwise open a turn of its own. Each control token's
// text found in it gets a zero-width space after its first byte.
func (m *llamaModel) neutralize(text string) string {
	for range 8 {
		found := false
		for _, tok := range llama.Tokenize(m.vocab, text, false, true) {
			if !m.special(tok) {
				continue
			}
			piece := llama.VocabGetText(m.vocab, tok)
			if len(piece) < 2 || !strings.Contains(text, piece) {
				continue
			}
			text = strings.ReplaceAll(text, piece, piece[:1]+"\u200b"+piece[1:])
			found = true
		}
		if !found {
			return text
		}
	}
	return text
}

// special reports whether tok is a control or user-defined token, which only the template
// may produce.
func (m *llamaModel) special(tok llama.Token) bool {
	return llama.VocabIsControl(m.vocab, tok) || llama.VocabGetAttr(m.vocab, tok)&llama.TokenAttrUserDef != 0
}

func (m *llamaModel) tokenize(prompt string) ([]int32, error) {
	tokens := llama.Tokenize(m.vocab, prompt, true, true)
	if len(tokens) == 0 {
		return nil, errors.New("the prompt tokenized to nothing")
	}
	out := make([]int32, len(tokens))
	for i, t := range tokens {
		out[i] = int32(t)
	}
	return out, nil
}

func (m *llamaModel) generate(ctx context.Context, prompt []int32, maxTokens int, temperature float32, emit func([]byte) bool) (int, error) {
	mem, err := llama.GetMemory(m.ctx)
	if err != nil {
		return 0, err
	}
	// Each call starts from an empty cache: nothing of the last call's prompt is reused.
	if err := llama.MemoryClear(mem, true); err != nil {
		return 0, err
	}
	sampler := llama.SamplerChainInit(llama.SamplerChainDefaultParams())
	defer llama.SamplerFree(sampler)
	if temperature == 0 {
		llama.SamplerChainAdd(sampler, llama.SamplerInitGreedy())
	} else {
		llama.SamplerChainAdd(sampler, llama.SamplerInitTopK(40))
		llama.SamplerChainAdd(sampler, llama.SamplerInitTopP(0.95, 1))
		llama.SamplerChainAdd(sampler, llama.SamplerInitTemp(temperature))
		llama.SamplerChainAdd(sampler, llama.SamplerInitDist(llama.DefaultSeed))
	}

	tokens := make([]llama.Token, len(prompt))
	for i, t := range prompt {
		tokens[i] = llama.Token(t)
	}
	batch := llama.BatchGetOne(tokens)
	piece := make([]byte, 256)
	for n := 0; n < maxTokens; n++ {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		rc, err := llama.Decode(m.ctx, batch)
		if err != nil {
			return n, fmt.Errorf("llama.cpp could not decode: %w", err)
		}
		if rc != 0 {
			return n, fmt.Errorf("llama.cpp could not decode: status %d", rc)
		}
		tok := llama.SamplerSample(sampler, m.ctx, -1)
		if llama.VocabIsEOG(m.vocab, tok) {
			return n, nil
		}
		l := llama.TokenToPiece(m.vocab, tok, piece, 0, false)
		if l < 0 {
			piece = make([]byte, -l)
			l = llama.TokenToPiece(m.vocab, tok, piece, 0, false)
		}
		if l > 0 && !emit(piece[:l]) {
			return n + 1, nil
		}
		batch = llama.BatchGetOne([]llama.Token{tok})
	}
	return maxTokens, nil
}

func (m *llamaModel) close() {
	_ = llama.Free(m.ctx)
	_ = llama.ModelFree(m.model)
}
