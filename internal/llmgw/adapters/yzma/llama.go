package yzma

import (
	"context"
	"errors"
	"fmt"

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
		msgs = append(msgs, llama.NewChatMessage(t.role, t.text))
		size += 2 * (len(t.role) + len(t.text))
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
