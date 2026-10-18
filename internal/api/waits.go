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
		// §5.33: the operator's inventory and cancellation, every kind, under `wait_admin`
		// (delta `2026-10-wait-monitoring`, decisions 3-5).
		"wait.list": {
			handle: handleWaitList, kinds: treeClients, available: hasWaits, capability: capabilityWaitAdmin,
			params: emptyResult{}, result: waitListResult{},
		},
		"wait.cancel": {
			handle: handleWaitCancel, kinds: treeClients, available: hasWaits, capability: capabilityWaitAdmin,
			params: waitCancelParams{}, result: emptyResult{},
		},
	}
}

const capabilityWaitAdmin = "wait_admin"

// waitTarget is a listed wait's target: one of the three is set.
type waitTarget struct {
	ThreadID  string `json:"thread_id,omitempty"`
	BlockID   string `json:"block_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type waitItem struct {
	WaitID       string     `json:"wait_id"`
	ConnectionID string     `json:"connection_id"`
	Target       waitTarget `json:"target"`
	Until        []string   `json:"until"`
	Regex        *string    `json:"regex"`
	AgeMS        int64      `json:"age_ms"`
	Stalled      bool       `json:"stalled"`
}

type waitListResult struct {
	Items []waitItem `json:"items"`
}

type waitCancelParams struct {
	WaitID string `json:"wait_id"`
}

func handleWaitList(_ context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := decode(raw, &emptyResult{}, "wait.list"); err != nil {
		return nil, err
	}
	items := []waitItem{}
	for _, w := range c.server.cfg.Waits.List() {
		until := w.Until
		if until == nil {
			until = []string{}
		}
		items = append(items, waitItem{
			WaitID: w.ID, ConnectionID: w.ConnectionID,
			Target: waitTarget{ThreadID: w.ThreadID, BlockID: w.BlockID, SessionID: w.SessionID},
			Until:  until, Regex: nullable(w.Regex), AgeMS: w.AgeMS, Stalled: w.Stalled,
		})
	}
	return waitListResult{Items: items}, nil
}

func handleWaitCancel(_ context.Context, c *conn, raw json.RawMessage) (any, error) {
	var p waitCancelParams
	if err := decode(raw, &p, "wait.cancel"); err != nil {
		return nil, err
	}
	if p.WaitID == "" {
		return nil, ValidationError("wait_id is required", ErrorField{Field: "wait_id", Issue: requiredTag})
	}
	if err := c.server.cfg.Waits.Cancel(p.WaitID); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
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
	w, err := c.server.cfg.Waits.Thread(ctx, c.connectionID, p.ThreadID, p.Until, p.TimeoutMS)
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
	w, err := c.server.cfg.Waits.Output(ctx, c.connectionID, waitsdomain.OutputParams{
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
	case errors.Is(err, waitsdomain.ErrCancelled):
		return codeCancelled, domainCancelled, true
	case errors.Is(err, waitsdomain.ErrNotFound):
		return codeNotFound, domainNotFound, true
	default:
		return 0, "", false
	}
}
