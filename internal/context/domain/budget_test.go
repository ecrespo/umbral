package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	llm "github.com/ecrespo/umbral/internal/llmgw/domain"
)

// history is n exchanges of a user message and an assistant answer, each of size bytes.
func history(n, size int) []llm.Message {
	out := make([]llm.Message, 0, 2*n)
	for i := range n {
		out = append(out,
			llm.Message{Role: llm.RoleUser, Text: fmt.Sprintf("q%03d ", i) + strings.Repeat("u", size)},
			llm.Message{Role: llm.RoleAssistant, Text: fmt.Sprintf("a%03d ", i) + strings.Repeat("a", size)},
		)
	}
	return out
}

type recorder struct {
	calls      int
	transcript string
	summary    string
	err        error
}

func (r *recorder) summarize(_ context.Context, transcript string) (string, error) {
	r.calls++
	r.transcript = transcript
	return r.summary, r.err
}

func TestCompactionTriggeredOverWindow_REQ_CTX_004(t *testing.T) {
	msgs := append(history(30, 300), llm.Message{Role: llm.RoleUser, Text: "and now fix the parser"})
	req := llm.Request{System: "you are an agent", Messages: msgs, MaxOutputTokens: 500}
	budget := Budget{Window: 4000, Reserve: 500}
	if EstimateRequest(req) <= budget.Available() {
		t.Fatal("the fixture must exceed the window minus the reserve")
	}
	rec := &recorder{summary: "the user asked about q000 to q020; nothing is fixed yet"}

	out, compacted, err := Compact(t.Context(), req, "", budget, rec.summarize)
	if err != nil {
		t.Fatal(err)
	}
	if compacted == nil || rec.calls != 1 {
		t.Fatalf("over the window, the history is summarized once: %v calls, %+v", rec.calls, compacted)
	}
	if compacted.BeforeTokens != EstimateRequest(req) || compacted.AfterTokens != EstimateRequest(out) {
		t.Fatalf("the event carries the estimates before and after: %+v", compacted)
	}
	if compacted.AfterTokens > budget.Available() {
		t.Fatalf("after compaction the request fits: %d > %d", compacted.AfterTokens, budget.Available())
	}
	if !strings.Contains(out.System, rec.summary) || !strings.HasPrefix(out.System, "you are an agent") {
		t.Fatalf("the summary joins the system prompt:\n%s", out.System)
	}
	if last := out.Messages[len(out.Messages)-1]; last.Text != "and now fix the parser" {
		t.Fatalf("the current message is kept: %q", last.Text)
	}
	if !strings.Contains(rec.transcript, "q000") || strings.Contains(rec.transcript, "and now fix the parser") {
		t.Fatal("the oldest messages are summarized, the current one is not")
	}
	if len(out.Messages) >= len(req.Messages) || len(out.Messages) < 2 {
		t.Fatalf("kept %d of %d messages", len(out.Messages), len(req.Messages))
	}
	if len(req.Messages) != len(msgs) || req.System != "you are an agent" {
		t.Fatal("the caller's request is not modified")
	}
	// The kept messages are the most recent ones, in order.
	tail := req.Messages[len(req.Messages)-len(out.Messages):]
	for i := range tail {
		if tail[i].Text != out.Messages[i].Text {
			t.Fatalf("kept message %d is not the original's tail", i)
		}
	}
}

func TestNoCompactionWithinTheWindow_REQ_CTX_004(t *testing.T) {
	req := llm.Request{System: "s", Messages: history(2, 50)}
	rec := &recorder{summary: "x"}
	for _, b := range []Budget{{Window: 4000, Reserve: 500}, {Window: 0}} {
		out, compacted, err := Compact(t.Context(), req, "", b, rec.summarize)
		if err != nil || compacted != nil || rec.calls != 0 || len(out.Messages) != 4 {
			t.Fatalf("%+v: within the window (or with no known window) nothing is compacted: %v %+v", b, err, compacted)
		}
	}
}

func TestCompactionNeverOrphansAToolResult_REQ_CTX_004(t *testing.T) {
	msgs := make([]llm.Message, 0, 81)
	for i := range 20 {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleUser, Text: strings.Repeat("u", 200)},
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: fmt.Sprint("c", i), Name: "read_file", Input: `{"path":"a"}`}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: fmt.Sprint("c", i), Text: strings.Repeat("t", 300)},
			llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("a", 100)},
		)
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: "go on"})
	// Try every window, so the cut lands on every position once.
	for window := int64(1200); window < 4500; window += 37 {
		out, c, err := Compact(t.Context(), llm.Request{Messages: msgs}, "", Budget{Window: window, Reserve: 100}, (&recorder{summary: "s"}).summarize)
		if err != nil {
			t.Fatalf("window %d: %v", window, err)
		}
		if c == nil {
			t.Fatalf("window %d: the fixture must be compacted", window)
		}
		if out.Messages[0].Role != llm.RoleUser {
			t.Fatalf("window %d: the kept history starts with %s, not a user message", window, out.Messages[0].Role)
		}
		for i, m := range out.Messages {
			if m.Role == llm.RoleTool && (i == 0 || len(out.Messages[i-1].ToolCalls) == 0 && out.Messages[i-1].Role != llm.RoleTool) {
				t.Fatalf("window %d: a tool result kept without its call", window)
			}
		}
	}
}

func TestASummaryFailureFailsTheCompaction_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: append(history(30, 300), llm.Message{Role: llm.RoleUser, Text: "now"})}
	boom := errors.New("no fast model")
	_, _, err := Compact(t.Context(), req, "", Budget{Window: 3000, Reserve: 300}, (&recorder{err: boom}).summarize)
	if !errors.Is(err, boom) || !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("a failed summary is a context overflow, with its cause: %v", err)
	}
}

func TestAnOverlongSummaryIsCut_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: append(history(30, 300), llm.Message{Role: llm.RoleUser, Text: "now"})}
	b := Budget{Window: 3000, Reserve: 300}
	out, c, err := Compact(t.Context(), req, "", b, (&recorder{summary: strings.Repeat("s", 50000)}).summarize)
	if err != nil {
		t.Fatal(err)
	}
	if c.AfterTokens > b.Available() || EstimateRequest(out) > b.Available() {
		t.Fatalf("a summary longer than its share is cut to fit: %d > %d", c.AfterTokens, b.Available())
	}
}

func TestWhatCannotFitIsAnError_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: strings.Repeat("x", 30000)}}}
	_, _, err := Compact(t.Context(), req, "", Budget{Window: 2000, Reserve: 200}, (&recorder{summary: "s"}).summarize)
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("a current message larger than the window cannot be compacted: %v", err)
	}
}

func TestTheSummaryInputIsBounded_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: append(history(40, 400), llm.Message{Role: llm.RoleUser, Text: "now"})}
	rec := &recorder{summary: "s"}
	if _, _, err := Compact(t.Context(), req, "", Budget{Window: 3000, Reserve: 300, SummaryInputTokens: 1000}, rec.summarize); err != nil {
		t.Fatal(err)
	}
	if EstimateText(rec.transcript) > 1000 {
		t.Fatalf("the transcript handed to the fast model is %d tokens, over its bound", EstimateText(rec.transcript))
	}
	if !strings.Contains(rec.transcript, "omitted") {
		t.Fatal("a clipped transcript says so")
	}
}

func TestReserveDefaults_REQ_CTX_004(t *testing.T) {
	for _, c := range []struct{ window, maxOut, want int64 }{
		{window: 65536, maxOut: 0, want: 8192},
		{window: 8192, maxOut: 0, want: 2048},
		{window: 32768, maxOut: 1000, want: 1000},
		{window: 0, maxOut: 0, want: 0},
		{window: 4096, maxOut: 100000, want: 2048},
	} {
		if got := NewBudget(c.window, c.maxOut).Reserve; got != c.want {
			t.Errorf("window %d max_output %d: reserve %d, want %d", c.window, c.maxOut, got, c.want)
		}
	}
}

func TestEstimatesAreConservative_REQ_CTX_004(t *testing.T) {
	// Three bytes a token over-counts English (about four) and code (three to four), and a
	// CJK character is three bytes and about one token.
	if got := EstimateText(strings.Repeat("abc", 100)); got != 100 {
		t.Fatalf("300 bytes estimate %d tokens, want 100", got)
	}
	if EstimateText("ab") != 1 || EstimateText("") != 0 {
		t.Fatal("estimates round up")
	}
	withTools := llm.Request{Tools: []llm.ToolSpec{{Name: "grep", Description: strings.Repeat("d", 300)}}}
	if EstimateRequest(withTools) < 100 {
		t.Fatal("tool specs count toward the context")
	}
}

func TestAMessageCostsMoreThanItsText_REQ_CTX_004(t *testing.T) {
	if EstimateMessage(llm.Message{Role: llm.RoleUser}) != 4 {
		t.Fatal("an empty message still costs its role and delimiters")
	}
	call := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{Name: "grep", Input: strings.Repeat("i", 300)}}}
	if EstimateMessage(call) < 100 {
		t.Fatal("tool call arguments count toward the context")
	}
}

func TestExactlyTheAvailableBudgetFits_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: strings.Repeat("x", 3*96)}}}
	b := Budget{Window: 150, Reserve: 50}
	if EstimateRequest(req) != b.Available() {
		t.Fatalf("fixture: %d != %d", EstimateRequest(req), b.Available())
	}
	rec := &recorder{summary: "s"}
	if _, c, err := Compact(t.Context(), req, "", b, rec.summarize); err != nil || c != nil || rec.calls != 0 {
		t.Fatalf("a request of exactly the available budget is not compacted: %+v %v", c, err)
	}
}

func TestAnOversizedSystemPromptCallsNoModel_REQ_CTX_004(t *testing.T) {
	req := llm.Request{System: strings.Repeat("s", 9000), Messages: history(3, 30)}
	rec := &recorder{summary: "s"}
	_, _, err := Compact(t.Context(), req, "", Budget{Window: 2000, Reserve: 200}, rec.summarize)
	if !errors.Is(err, ErrContextOverflow) || rec.calls != 0 {
		t.Fatalf("when what must stay does not fit, no summary is asked for: %v, %d calls", err, rec.calls)
	}
}

func TestASecondCompactionReplacesTheSummary_REQ_CTX_004(t *testing.T) {
	b := Budget{Window: 4000, Reserve: 500}
	req := llm.Request{System: "base", Messages: append(history(30, 300), llm.Message{Role: llm.RoleUser, Text: "one"})}
	first := &recorder{summary: strings.Repeat("first ", 20)}
	out, c1, err := Compact(t.Context(), req, "", b, first.summarize)
	if err != nil || c1 == nil {
		t.Fatalf("first compaction: %+v %v", c1, err)
	}
	// The runtime keeps the base prompt, the summary and the kept messages, and the thread goes on.
	next := llm.Request{System: "base", Messages: append(append([]llm.Message(nil), out.Messages...), history(30, 300)...)}
	next.Messages = append(next.Messages, llm.Message{Role: llm.RoleUser, Text: "two"})
	second := &recorder{summary: "second"}
	out2, c2, err := Compact(t.Context(), next, c1.Summary, b, second.summarize)
	if err != nil || c2 == nil {
		t.Fatalf("second compaction: %+v %v", c2, err)
	}
	if strings.Count(out2.System, "# Earlier conversation") != 1 || strings.Contains(out2.System, "first first") {
		t.Fatalf("the new summary replaces the old one:\n%s", out2.System)
	}
	if !strings.Contains(second.transcript, c1.Summary) {
		t.Fatal("the previous summary is summarized again, not lost")
	}
}

func TestAPreviousSummaryThatStillFitsIsCarried_REQ_CTX_004(t *testing.T) {
	req := llm.Request{System: "base", Messages: history(1, 10)}
	out, c, err := Compact(t.Context(), req, "what came before", Budget{Window: 4000, Reserve: 500}, (&recorder{}).summarize)
	if err != nil || c != nil || !strings.Contains(out.System, "what came before") {
		t.Fatalf("a request that fits still carries the summary: %v %+v\n%s", err, c, out.System)
	}
}

func TestMidTurnTheUsersRequestIsKept_REQ_CTX_004(t *testing.T) {
	msgs := make([]llm.Message, 0, 83)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: "old"}, llm.Message{Role: llm.RoleAssistant, Text: "ok"}, llm.Message{Role: llm.RoleUser, Text: "fix the parser"})
	for i := range 40 {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: fmt.Sprint("c", i), Name: "grep", Input: `{"pattern":"x"}`}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: fmt.Sprint("c", i), Text: strings.Repeat("r", 200)},
		)
	}
	out, c, err := Compact(t.Context(), llm.Request{Messages: msgs}, "", Budget{Window: 3000, Reserve: 300}, (&recorder{summary: "s"}).summarize)
	if err != nil || c == nil {
		t.Fatalf("%+v %v", c, err)
	}
	if out.Messages[0].Role != llm.RoleUser || out.Messages[0].Text != "fix the parser" {
		t.Fatalf("the user's current request is kept verbatim first, got %+v", out.Messages[0])
	}
	if last := out.Messages[len(out.Messages)-1]; last.ToolCallID != "c39" {
		t.Fatalf("and the newest results after it: %+v", last)
	}
	if out.Messages[1].Role == llm.RoleTool {
		t.Fatal("a tool result follows the request without its call")
	}
}

func TestACutSummarySaysSo_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: append(history(30, 300), llm.Message{Role: llm.RoleUser, Text: "now"})}
	_, c, err := Compact(t.Context(), req, "", Budget{Window: 3000, Reserve: 300}, (&recorder{summary: strings.Repeat("s", 50000)}).summarize)
	if err != nil || !strings.HasSuffix(c.Summary, "[summary cut to fit]") {
		t.Fatalf("%v %q", err, c.Summary[len(c.Summary)-40:])
	}
}

func TestMidTurnEveryCutKeepsCallsWithResults_REQ_CTX_004(t *testing.T) {
	// A large request and a summary that takes its whole share leave no slack to hide in.
	msgs := make([]llm.Message, 0, 81)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Text: "fix the parser " + strings.Repeat("p", 600)})
	for i := range 40 {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("t", i%7*20), ToolCalls: []llm.ToolCall{{ID: fmt.Sprint("c", i), Name: "grep", Input: `{"pattern":"x"}`}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: fmt.Sprint("c", i), Text: strings.Repeat("r", 150+i%5*40)},
		)
	}
	for window := int64(900); window < 3600; window += 23 {
		b := Budget{Window: window, Reserve: 100}
		out, c, err := Compact(t.Context(), llm.Request{Messages: msgs}, "", b, (&recorder{summary: strings.Repeat("s", 50000)}).summarize)
		if err != nil {
			t.Fatalf("window %d: %v", window, err)
		}
		if c == nil || c.AfterTokens > b.Available() {
			t.Fatalf("window %d: compacted %+v does not fit %d", window, c, b.Available())
		}
		if out.Messages[0].Role != llm.RoleUser {
			t.Fatalf("window %d: starts with %s", window, out.Messages[0].Role)
		}
		for i := 1; i < len(out.Messages); i++ {
			if out.Messages[i].Role == llm.RoleTool && len(out.Messages[i-1].ToolCalls) == 0 {
				t.Fatalf("window %d: message %d is a tool result kept without its call", window, i)
			}
		}
	}
}

func TestAHistoryWithNoUserMessageThatCannotFitOverflows_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleAssistant, Text: strings.Repeat("a", 30000)}}}
	if _, _, err := Compact(t.Context(), req, "", Budget{Window: 2000, Reserve: 200}, (&recorder{summary: "s"}).summarize); !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("an empty history is never what is sent: %v", err)
	}
}

func TestEverythingSentIsEstimated_REQ_CTX_004(t *testing.T) {
	result := llm.Message{Role: llm.RoleTool, ToolCallID: strings.Repeat("i", 30)}
	if EstimateMessage(result) != 4+10 {
		t.Fatalf("a tool result's call id is sent: %d", EstimateMessage(result))
	}
	schema := llm.Request{ResponseSchema: map[string]any{"description": strings.Repeat("d", 300)}}
	if EstimateRequest(schema) < 100 {
		t.Fatal("a response schema is sent and counts")
	}
}

func TestACappedReserveCapsTheAnswer_REQ_CTX_004(t *testing.T) {
	b := NewBudget(8192, 6000)
	req := llm.Request{MaxOutputTokens: 6000, Messages: history(1, 10)}
	out, _, err := Compact(t.Context(), req, "", b, (&recorder{}).summarize)
	if err != nil {
		t.Fatal(err)
	}
	if out.MaxOutputTokens != b.Reserve || EstimateRequest(out)+out.MaxOutputTokens > b.Window {
		t.Fatalf("the request asks for %d output tokens with a reserve of %d", out.MaxOutputTokens, b.Reserve)
	}
}

func TestAPreviousSummaryCountsTowardTheSummaryInput_REQ_CTX_004(t *testing.T) {
	req := llm.Request{Messages: append(history(40, 400), llm.Message{Role: llm.RoleUser, Text: "now"})}
	rec := &recorder{summary: "s"}
	_, _, err := Compact(t.Context(), req, strings.Repeat("previous ", 3000), Budget{Window: 3000, Reserve: 300, SummaryInputTokens: 1000}, rec.summarize)
	if err != nil {
		t.Fatal(err)
	}
	if got := EstimateText(rec.transcript); got > 1000+EstimateText(summaryCutMark) {
		t.Fatalf("the summarizing model reads %d tokens, over its bound", got)
	}
	if !strings.Contains(rec.transcript, "previous previous") {
		t.Fatal("the previous summary is still part of what is summarized")
	}
}

func TestACancelDuringTheSummaryIsACancel_REQ_CTX_004(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	req := llm.Request{Messages: append(history(30, 300), llm.Message{Role: llm.RoleUser, Text: "now"})}
	_, _, err := Compact(ctx, req, "", Budget{Window: 3000, Reserve: 300}, func(context.Context, string) (string, error) {
		cancel()
		return "", context.Canceled
	})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrContextOverflow) {
		t.Fatalf("a cancelled turn is not an overflow: %v", err)
	}
}
