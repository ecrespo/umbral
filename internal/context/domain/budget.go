package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
)

// Token estimation (REQ-CTX-004, Tech Q-03). No model's tokenizer is loaded: a family's BPE
// tables cost tens of MiB of the daemon's memory and still miss every other family. The
// estimate is instead deliberately high — a token per three bytes, where English runs about
// four and code three to four, and a CJK character is three bytes and about one token — so
// that the error compacts a little early rather than overflow a window.
const (
	bytesPerToken = 3
	// messageOverhead is what a message costs besides its text: its role and delimiters.
	messageOverhead = 4
	// defaultReserveCap bounds the response reserve when the call sets no max output.
	defaultReserveCap = 8192
)

// EstimateText is the estimated token count of a string.
func EstimateText(s string) int64 {
	return int64((len(s) + bytesPerToken - 1) / bytesPerToken)
}

// EstimateMessage is the estimated token count of one message.
func EstimateMessage(m llm.Message) int64 {
	n := messageOverhead + EstimateText(m.Text) + EstimateText(m.ToolName) + EstimateText(m.ToolCallID)
	for _, tc := range m.ToolCalls {
		n += EstimateText(tc.Name) + EstimateText(tc.Input)
	}
	return n
}

// EstimateRequest is the estimated token count of everything a request sends: the system
// prompt, the tools offered and the messages.
func EstimateRequest(req llm.Request) int64 {
	n := EstimateText(req.System)
	for _, t := range req.Tools {
		n += EstimateText(t.Name) + EstimateText(t.Description)
		if schema, err := json.Marshal(t.InputSchema); err == nil {
			n += EstimateText(string(schema))
		}
	}
	if req.ResponseSchema != nil {
		if schema, err := json.Marshal(req.ResponseSchema); err == nil {
			n += EstimateText(string(schema))
		}
	}
	for _, m := range req.Messages {
		n += EstimateMessage(m)
	}
	return n
}

// Budget is a model's window and the share of it held back for the answer.
type Budget struct {
	// Window is the model's context window in tokens; 0 when unknown, and then nothing is
	// compacted.
	Window int64
	// Reserve is the response reserve.
	Reserve int64
	// SummaryInputTokens bounds the transcript handed to the summarizing model; 0 is no bound.
	SummaryInputTokens int64
}

// NewBudget is a model's budget: the reserve is the call's max output tokens when it sets
// them, otherwise a quarter of the window up to 8192, and never more than half the window.
func NewBudget(window, maxOutputTokens int64) Budget {
	reserve := maxOutputTokens
	if reserve <= 0 {
		reserve = min(window/4, defaultReserveCap)
	}
	if window > 0 && reserve > window/2 {
		// A max output as large as the window would leave the request nothing.
		reserve = window / 2
	}
	return Budget{Window: window, Reserve: reserve}
}

// Available is what the request may use: the window minus the reserve.
func (b Budget) Available() int64 { return b.Window - b.Reserve }

// Compacted is what a compaction did — the `context.compacted` notification's payload
// besides the thread (API §7).
type Compacted struct {
	BeforeTokens int64
	AfterTokens  int64
	// Summarized is how many of the oldest messages the summary covers; the user's latest
	// request among them is kept verbatim as well.
	Summarized int
	// Summary replaces any earlier one.
	Summary string
}

// ErrContextOverflow is a request that cannot fit its window even compacted: what must be
// kept — the system prompt, the tools and the current message — is larger than the window.
var ErrContextOverflow = errors.New("the context does not fit the model's window")

// Summarize turns a transcript into a summary; the agent runtime calls the `fast` class.
type Summarize func(ctx context.Context, transcript string) (string, error)

// summaryShare is the part of the available budget a summary may take.
const summaryShare = 5

// Compact fits a request to its budget (REQ-CTX-004). req.System is the base prompt, without a
// summary; summary is the one a previous compaction left, "" when there is none, and it rides
// in the system prompt under a header (the runtime keeps it and the messages after it, Tech
// §5.3c). While the whole fits, Compact returns it with nothing done.
//
// Over the budget it keeps the newest messages that fit and summarizes the older ones, together
// with the previous summary, which the new one replaces rather than joins: the prompt holds one
// summary however many times a thread is compacted. The kept history starts with a user
// message: the user's latest request is kept verbatim even when it lies in the summarized part —
// mid-turn the newest messages are tool calls and results — and a tool result is never kept
// without its call. The caller's request is not modified.
//
// What must stay not fitting, and a summary that cannot be made, are ErrContextOverflow.
func Compact(ctx context.Context, req llm.Request, summary string, b Budget, summarize Summarize) (llm.Request, *Compacted, error) {
	if b.Window > 0 && req.MaxOutputTokens > b.Reserve {
		// The reserve was capped below what the call asked for: the answer gets the reserve,
		// or the request and its answer together would not fit the window.
		req.MaxOutputTokens = b.Reserve
	}
	whole := withSummary(req, summary)
	before := EstimateRequest(whole)
	if b.Window <= 0 || before <= b.Available() {
		return whole, nil, nil
	}
	msgs := req.Messages
	fixed := EstimateRequest(llm.Request{System: req.System, Tools: req.Tools, ResponseSchema: req.ResponseSchema})
	summaryBudget := b.Available() / summaryShare
	// The joined system prompt costs at most its parts' estimates, so what is kept below fits
	// by construction.
	keepBudget := b.Available() - fixed - summaryBudget - EstimateText(summarySeparator+summaryHeader)

	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			lastUser = i
			break
		}
	}
	cut, used := len(msgs), int64(0)
	for cut > 0 && used+EstimateMessage(msgs[cut-1]) <= keepBudget {
		cut--
		used += EstimateMessage(msgs[cut])
	}
	var kept []llm.Message
	if lastUser >= cut {
		// The request is among the kept messages: start there, or at a later user message.
		for msgs[cut].Role != llm.RoleUser {
			cut++
		}
		kept = msgs[cut:]
	} else {
		// Keep the request itself, and as many of the newest messages as fit beside it.
		pin := int64(0)
		if lastUser >= 0 {
			pin = EstimateMessage(msgs[lastUser])
		}
		for cut < len(msgs) && (used+pin > keepBudget || msgs[cut].Role == llm.RoleTool) {
			used -= EstimateMessage(msgs[cut])
			cut++
		}
		if pin > keepBudget || (lastUser < 0 && cut == len(msgs)) {
			return llm.Request{}, nil, fmt.Errorf("%w: %d tokens estimated, %d available", ErrContextOverflow, before, b.Available())
		}
		if lastUser >= 0 {
			kept = append(kept, msgs[lastUser])
		}
		kept = append(kept, msgs[cut:]...)
	}

	next, err := summarize(ctx, summaryInput(msgs[:cut], summary, b.SummaryInputTokens))
	if err != nil {
		if ctx.Err() != nil {
			return llm.Request{}, nil, ctx.Err() // a cancelled turn, not an overflow
		}
		return llm.Request{}, nil, fmt.Errorf("%w: summarizing the history: %w", ErrContextOverflow, err)
	}
	if limit := summaryBudget * bytesPerToken; int64(len(next)) > limit {
		if limit > int64(len(summaryCutMark)) {
			next = string(cut3(next, limit-int64(len(summaryCutMark)))) + summaryCutMark
		} else {
			next = string(cut3(next, limit))
		}
	}

	out := req
	out.Messages = append([]llm.Message(nil), kept...)
	out = withSummary(out, next)
	return out, &Compacted{BeforeTokens: before, AfterTokens: EstimateRequest(out), Summarized: cut, Summary: next}, nil
}

// withSummary is req with summary under its header in the system prompt.
func withSummary(req llm.Request, summary string) llm.Request {
	if summary == "" {
		return req
	}
	req.System = strings.TrimRight(req.System, "\n") + summarySeparator + summaryHeader + summary
	return req
}

const (
	summarySeparator = "\n\n"
	summaryHeader    = "# Earlier conversation\nThe oldest part of this conversation was compacted to fit the model's window. Its summary:\n"
	summaryCutMark   = " [summary cut to fit]"
)

// cut3 cuts s to at most n bytes on a rune boundary.
func cut3(s string, n int64) []byte {
	if n < 0 {
		n = 0
	}
	return cut([]byte(s), int(n))
}

// summaryInput is what the summarizing model reads: the previous summary, then the transcript
// of the newly summarized messages, within limit tokens when limit is set. The previous summary
// may take at most half of it.
func summaryInput(msgs []llm.Message, previous string, limit int64) string {
	if previous == "" {
		return transcript(msgs, limit)
	}
	if limit > 0 && EstimateText(previous) > limit/2 {
		previous = string(cut3(previous, limit/2*bytesPerToken)) + summaryCutMark
	}
	prefix := "Summary of the conversation before these messages:\n" + previous + "\n\n"
	rest := int64(0)
	if limit > 0 {
		rest = max(limit-EstimateText(prefix), 1)
	}
	return prefix + transcript(msgs, rest)
}

// transcript renders messages for the summarizing model, keeping the most recent part within
// limit tokens when limit is set.
func transcript(msgs []llm.Message, limit int64) string {
	var s strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleTool:
			fmt.Fprintf(&s, "tool result (%s):\n%s\n\n", m.ToolName, m.Text)
		default:
			fmt.Fprintf(&s, "%s:\n%s\n", m.Role, m.Text)
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&s, "[called %s %s]\n", tc.Name, tc.Input)
			}
			s.WriteString("\n")
		}
	}
	out := s.String()
	if limit <= 0 || EstimateText(out) <= limit {
		return out
	}
	note := "[the oldest %d bytes of the transcript are omitted]\n"
	keep := int(limit*bytesPerToken) - len(note) - 20
	if keep < 0 {
		keep = 0
	}
	start := len(out) - keep
	for start < len(out) && !utf8Start(out[start]) {
		start++
	}
	return fmt.Sprintf(note, start) + out[start:]
}

// utf8Start reports whether b begins a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
