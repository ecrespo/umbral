package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	waitsdomain "github.com/ecrespo/umbral/internal/waits/domain"
)

// capabilityWaits is §2's namespace for thread.wait and block.wait_output, whose prefixes
// name other ones.
const capabilityWaits = "waits"

// hasWaits is the wait engine's availability.
func hasWaits(cfg Config) bool { return cfg.Waits != nil }

// waitMethods is §5.29 and §5.30. thread.wait is the interactive clients' (§2's cli row
// does not name it); block.wait_output is block.*, which every kind may call.
func waitMethods() map[string]method {
	return map[string]method{
		"thread.wait": {
			handle: handleThreadWait, kinds: interactiveClients, available: hasWaits,
			params: threadWaitParams{}, result: threadWaitResult{},
		},
		"block.wait_output": {
			handle: handleWaitOutput, available: hasWaits,
			params: waitOutputParams{}, result: waitOutputResult{},
		},
	}
}

type threadWaitParams struct {
	ThreadID  string   `json:"thread_id"`
	Until     []string `json:"until"`
	TimeoutMS int64    `json:"timeout_ms"`
}

type threadWaitResult struct {
	ThreadID string  `json:"thread_id"`
	TurnID   *string `json:"turn_id"`
	State    string  `json:"state"`
	WaitedMS int64   `json:"waited_ms"`
}

// handleThreadWait pins the thread's turn in progress before the next request is read, then
// waits beside the read loop (DD-011).
func handleThreadWait(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	var p threadWaitParams
	if err := decode(raw, &p, "thread.wait"); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, ValidationError("thread_id is required", ErrorField{Field: fieldThreadID, Issue: requiredTag})
	}
	w, err := c.server.cfg.Waits.Thread(ctx, p.ThreadID, p.Until, p.TimeoutMS)
	if err != nil {
		return nil, err
	}
	if err := w.PinCurrent(ctx); err != nil {
		w.Close()
		return nil, err
	}
	return deferred(func(ctx context.Context) (any, error) {
		got, err := w.Wait(ctx)
		if err != nil {
			return nil, err
		}
		return threadWaitResult{
			ThreadID: got.ThreadID, TurnID: nullable(got.TurnID), State: string(got.State), WaitedMS: got.WaitedMS,
		}, nil
	}), nil
}

type waitOutputParams struct {
	SessionID string `json:"session_id" api:"optional"`
	BlockID   string `json:"block_id" api:"optional"`
	Regex     string `json:"regex"`
	Lines     *int   `json:"lines" api:"optional"`
	TimeoutMS int64  `json:"timeout_ms"`
}

type waitOutputResult struct {
	BlockID     *string `json:"block_id"`
	MatchedLine string  `json:"matched_line"`
	LineNumber  int     `json:"line_number"`
}

func handleWaitOutput(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	var p waitOutputParams
	if err := decode(raw, &p, "block.wait_output"); err != nil {
		return nil, err
	}
	// Only an absent `lines` is the default; an explicit 0 is out of range (§5.30).
	lines := waitsdomain.DefaultLines
	if p.Lines != nil {
		if lines = *p.Lines; lines < 1 {
			return nil, ValidationError(fmt.Sprintf("lines must be 1-%d", waitsdomain.MaxLines),
				ErrorField{Field: "lines", Issue: fmt.Sprintf("must be between 1 and %d", waitsdomain.MaxLines)})
		}
	}
	w, err := c.server.cfg.Waits.Output(ctx, waitsdomain.OutputParams{
		SessionID: p.SessionID, BlockID: p.BlockID, Regex: p.Regex, Lines: lines, TimeoutMS: p.TimeoutMS,
	})
	if err != nil {
		return nil, err
	}
	return deferred(func(ctx context.Context) (any, error) {
		got, err := w.Wait(ctx)
		if err != nil {
			return nil, err
		}
		return waitOutputResult{BlockID: nullable(got.BlockID), MatchedLine: got.MatchedLine, LineNumber: got.LineNumber}, nil
	}), nil
}

// nullable is "" as JSON null.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// waitsDomainError maps the wait engine's sentinels; a timeout's last state is added to its
// data by finishWire.
func waitsDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, waitsdomain.ErrValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, waitsdomain.ErrTimeout):
		return codeTimeout, domainTimeout, true
	default:
		return 0, "", false
	}
}
