package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// Session is the wire shape of a session (API Spec §4).
type Session struct {
	ID            string  `json:"id"`
	Shell         string  `json:"shell"`
	CWD           string  `json:"cwd"`
	Cols          uint16  `json:"cols"`
	Rows          uint16  `json:"rows"`
	State         string  `json:"state"`
	Integration   string  `json:"integration"`
	InputOwner    string  `json:"input_owner"`
	OwnerThreadID *string `json:"owner_thread_id"`
	ExitCode      *int    `json:"exit_code"`
	CreatedAt     int64   `json:"created_at"`
	ExitedAt      *int64  `json:"exited_at"`
}

// toWireSession converts a domain session. Times become UTC epoch milliseconds and the
// optional fields become explicit nulls, both per Art. 6 and API Spec §4.
func toWireSession(s sessdomain.Session) Session {
	out := Session{
		ID: s.ID, Shell: s.Shell, CWD: s.CWD,
		Cols: s.Size.Cols, Rows: s.Size.Rows,
		State: string(s.State), Integration: string(s.Integration),
		InputOwner: string(s.InputOwner),
		ExitCode:   s.ExitCode,
		CreatedAt:  s.CreatedAt.UTC().UnixMilli(),
	}
	if s.OwnerThreadID != "" {
		owner := s.OwnerThreadID
		out.OwnerThreadID = &owner
	}
	if s.ExitedAt != nil {
		exited := s.ExitedAt.UTC().UnixMilli()
		out.ExitedAt = &exited
	}
	return out
}

type createSessionParams struct {
	Shell            string            `json:"shell"`
	CWD              string            `json:"cwd"`
	Env              map[string]string `json:"env"`
	Cols             uint16            `json:"cols"`
	Rows             uint16            `json:"rows"`
	ShellIntegration *bool             `json:"shell_integration"`
}

func handleSessionCreate(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if c.server.cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: session.create", ErrMethodNotFound)
	}

	var params createSessionParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, ValidationError("session.create parameters are not an object")
		}
	}

	// shell_integration defaults to true (API Spec §5.3), which a bare bool cannot express.
	integration := true
	if params.ShellIntegration != nil {
		integration = *params.ShellIntegration
	}

	session, err := c.server.cfg.Sessions.Create(ctx, sessdomain.CreateParams{
		Shell: params.Shell, CWD: params.CWD, Env: params.Env,
		Size:             sessdomain.Size{Cols: params.Cols, Rows: params.Rows},
		ShellIntegration: integration,
	})
	if err != nil {
		return nil, err
	}
	return toWireSession(session), nil
}

func handleSessionList(ctx context.Context, c *conn, _ json.RawMessage) (any, error) {
	if c.server.cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: session.list", ErrMethodNotFound)
	}

	sessions, err := c.server.cfg.Sessions.List(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, toWireSession(s))
	}
	return map[string]any{"items": items}, nil
}

type sessionInputParams struct {
	SessionID string `json:"session_id"`
	DataB64   string `json:"data_b64"`
}

func handleSessionInput(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if c.server.cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: session.input", ErrMethodNotFound)
	}

	var params sessionInputParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ValidationError("session.input parameters are not an object")
	}
	data, err := base64.StdEncoding.DecodeString(params.DataB64)
	if err != nil {
		return nil, ValidationError("data_b64 is not valid base64",
			ErrorField{Field: fieldDataB64, Issue: err.Error()})
	}

	// A client always types as the human. The agent writes through the tools module, not
	// through this socket, which is what makes the lock meaningful (DD-003).
	if err := c.server.cfg.Sessions.Input(ctx, params.SessionID, data, sessdomain.InputOwnerHuman); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

type sessionResizeParams struct {
	SessionID string `json:"session_id"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

func handleSessionResize(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if c.server.cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: session.resize", ErrMethodNotFound)
	}

	var params sessionResizeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ValidationError("session.resize parameters are not an object")
	}
	if err := c.server.cfg.Sessions.Resize(ctx, params.SessionID,
		sessdomain.Size{Cols: params.Cols, Rows: params.Rows}); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

type sessionIDParams struct {
	SessionID string `json:"session_id"`
}

func handleSessionClose(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if c.server.cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: session.close", ErrMethodNotFound)
	}

	var params sessionIDParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ValidationError("session.close parameters are not an object")
	}
	if err := c.server.cfg.Sessions.Close(ctx, params.SessionID); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

type sessionSubscribeParams struct {
	SessionID       string `json:"session_id"`
	ScrollbackLines *int   `json:"scrollback_lines"`
}

// handleSessionSubscribe sends the screen before the first live chunk (REQ-TERM-004).
//
// The order is the requirement. The subscription is registered *before* the snapshot is
// taken, so a chunk arriving between the two is queued rather than lost; the snapshot's
// sequence number then tells the subscription to discard anything it already contains.
// Registering afterwards would open a window in which output vanishes, and a terminal that
// silently loses a line is worse than one that repeats it.
func handleSessionSubscribe(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if c.server.cfg.Sessions == nil {
		return nil, fmt.Errorf("%w: session.subscribe", ErrMethodNotFound)
	}

	var params sessionSubscribeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ValidationError("session.subscribe parameters are not an object")
	}
	if params.ScrollbackLines != nil {
		if *params.ScrollbackLines < 0 || *params.ScrollbackLines > sessdomain.MaxScrollbackLines {
			return nil, ValidationError("scrollback_lines is out of range",
				ErrorField{Field: "scrollback_lines", Issue: "must be between 0 and 10000"})
		}
	}

	// Confirm the session exists before registering anything, so a typo does not leave a
	// live subscription to nothing.
	if _, err := c.server.cfg.Sessions.Get(ctx, params.SessionID); err != nil {
		return nil, err
	}

	// Subscribe before taking the snapshot, so output produced while it is being built is
	// queued instead of lost, then raise the floor to what the snapshot already shows. The
	// second step rebases the subscription rather than creating a new one: a replacement
	// would drop that queue and reopen the gap (REQ-TERM-004).
	c.subscribe(params.SessionID, 0)

	snapshot, err := c.server.cfg.Sessions.Snapshot(ctx, params.SessionID)
	if err != nil {
		c.unsubscribe(params.SessionID)
		return nil, err
	}
	c.rebaseSubscription(params.SessionID, snapshot.Seq)

	return map[string]any{
		"snapshot": map[string]any{
			"format":     "vt",
			fieldDataB64: base64.StdEncoding.EncodeToString(snapshot.Data),
			"cursor":     map[string]any{"x": snapshot.CursorX, "y": snapshot.CursorY},
		},
		"seq": snapshot.Seq,
	}, nil
}

func handleSessionUnsubscribe(_ context.Context, c *conn, raw json.RawMessage) (any, error) {
	var params sessionIDParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ValidationError("session.unsubscribe parameters are not an object")
	}
	c.unsubscribe(params.SessionID)
	// Unsubscribing from something you were not subscribed to is not an error: the client
	// wanted no subscription, and it has none.
	return map[string]any{}, nil
}

// sessionDomainError maps the sessions module's sentinels onto the protocol codes of API
// Spec §3. It lives here because api is the only package allowed to know the wire format
// (Tech Design §5.4), and the sessions module must not import it.
func sessionDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, sessdomain.ErrNotFound):
		return codeNotFound, "NOT_FOUND", true
	case errors.Is(err, sessdomain.ErrInputLocked):
		return codeInputLocked, "INPUT_LOCKED", true
	case errors.Is(err, sessdomain.ErrExited):
		return codeConflict, "CONFLICT", true
	case errors.Is(err, sessdomain.ErrValidation):
		return codeValidationError, "VALIDATION_ERROR", true
	default:
		return 0, "", false
	}
}
