package workspaces

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/workspaces/domain"
)

// Restore rebuilds the tree a previous run left (REQ-TERM-009).
//
// It runs once, from the composition root, before the socket accepts a connection. A client
// that reached a half-restored tree would watch panes appear one at a time with no
// notification explaining them, and `session.snapshot` would report a state that was true for
// a moment.
//
// What it does *not* do is the part that matters. **Every pane gets a fresh shell**, whatever
// it was running before. A pane with a stored command keeps it, marked pending, and the
// command is typed at the new shell's prompt without a newline — visible, waiting for the
// user to press Enter. REQ-TERM-011 exists because the alternative is a daemon that re-runs
// `terraform apply` or `make deploy` unattended on every start, possibly after a crash that
// command caused. Data Model §6 step 5 used to say the opposite; delta
// `2026-09-restore-semantics` settles it.
func (s *Service) Restore(ctx context.Context) error {
	restored, err := s.tree.Restore(ctx)
	if err != nil {
		return err
	}

	// Focus first, so a client connecting the instant the socket opens is told where to
	// look even while a pane's terminal is still starting.
	if restored.Focus.WorkspaceID != "" {
		s.adoptFocus(restored.Focus.WorkspaceID, restored.Focus.TabID)
	}
	if len(restored.Panes) == 0 {
		s.log.Info("no structure to restore")
		return nil
	}

	var attached, failed, pending int
	for _, pane := range restored.Panes {
		live, err := s.restorePane(ctx, pane)
		if err != nil {
			// One pane that cannot start must not cost the user the rest of the tree.
			// It stays in the database with no session, which is a state the schema
			// allows and which `panes.session_id IS NULL` already means everywhere else.
			failed++
			s.log.Error("restore: the pane's terminal did not start",
				slog.String("pane_id", pane.ID), slog.String("error", err.Error()))
			continue
		}
		attached++
		if live.CommandPending {
			pending++
		}
	}

	s.log.Info("structure restored",
		slog.Int("panes", attached), slog.Int("failed", failed),
		slog.Int("pending_commands", pending),
		slog.String("focused_workspace", restored.Focus.WorkspaceID),
		slog.String("focused_tab", restored.Focus.TabID))
	return nil
}

// restorePane gives one restored pane a fresh shell and hands its pending command to the
// session, which shows it at the prompt.
func (s *Service) restorePane(ctx context.Context, pane domain.Pane) (domain.Pane, error) {
	if s.terminals == nil {
		return pane, nil
	}

	params := sessdomain.CreateParams{
		Shell: s.shell, CWD: pane.CWD, Env: pane.Env, Size: defaultPaneSize,
		// A shell, never the stored command, and integration on: the pane is running a
		// shell now, so the block lifecycle applies to it — and the prompt marker is what
		// tells the session when it is safe to type the pending command.
		ShellIntegration: true,
	}
	if len(pane.Command) > 0 {
		pane.CommandPending = true
		params.TypeAtPrompt = []byte(shellLine(pane.Command))
	}
	// The screen the pane showed before, if the user opted in (REQ-TERM-010). It goes to
	// the emulator, before the shell writes anything, so a client subscribing sees what it
	// saw last time with the new prompt beneath it.
	params.ReplayScreen = s.restoreScreen(ctx, pane.ID)

	session, err := s.terminals.Create(ctx, params)
	if err != nil {
		return domain.Pane{}, fmt.Errorf("workspaces: restore %s: %w", pane.ID, err)
	}
	if err := s.tree.AttachSession(ctx, pane.ID, session.ID); err != nil {
		_ = s.terminals.Close(ctx, session.ID)
		return domain.Pane{}, err
	}
	pane.SessionID = session.ID
	return pane, nil
}

// shellLine renders an argv as a line a person would type.
//
// It is only ever shown, never executed by Umbral, so the quoting has one job: to be what the
// user's own shell would run if they pressed Enter. Single quotes with the escape that works
// inside them, which is the one form every POSIX shell agrees on.
func shellLine(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, quoteForShell(arg))
	}
	return strings.Join(parts, " ")
}

// quoteForShell wraps an argument so a shell reads it as one word.
//
// A bare word is left bare when it cannot be misread — quoting `ls` as `'ls'` is correct and
// unreadable, and this text exists to be read. Anything else is single-quoted, with an
// embedded single quote written the only way that works there: close, escape, reopen.
func safeInAShellWord(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '_', r == '.', r == '/', r == ':', r == '=':
		return true
	default:
		return false
	}
}

func quoteForShell(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool { return !safeInAShellWord(r) }) < 0 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// rememberFocus persists what the last focus call chose, so it survives a restart.
//
// A failure is logged and not returned: focus is a convenience, and refusing a
// `workspace.focus` because a write to one column failed would turn a cosmetic loss into a
// broken call. The in-memory value is already correct either way.
func (s *Service) rememberFocus(ctx context.Context, workspaceID, tabID string) {
	if err := s.tree.SetFocus(ctx, workspaceID, tabID, time.Now().UTC().UnixMilli()); err != nil {
		s.log.Warn("could not persist focus",
			slog.String("workspace_id", workspaceID), slog.String("tab_id", tabID),
			slog.String("error", err.Error()))
	}
}
