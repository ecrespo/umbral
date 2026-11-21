package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	acpdomain "github.com/ecrespo/umbral/internal/acp/domain"
	acpports "github.com/ecrespo/umbral/internal/acp/ports"
	"github.com/ecrespo/umbral/internal/api"
	"github.com/ecrespo/umbral/internal/config"
	"github.com/ecrespo/umbral/internal/sandbox/adapters/landlock"
	"github.com/ecrespo/umbral/internal/sandbox/adapters/seatbelt"
	sandboxdomain "github.com/ecrespo/umbral/internal/sandbox/domain"
	sandboxports "github.com/ecrespo/umbral/internal/sandbox/ports"
)

// exitSandbox is the helper's exit when it could not confine itself: nothing of the command
// it was given has run.
const exitSandbox = 126

// sandboxHelper is `umbrald --sandbox-exec <profile> -- <command> [args]`: the daemon's own
// binary confining itself and becoming the command (delta `2026-11-sandbox-linux`, decision
// 2). It reports whether args were the helper's, and then never returns on success.
func sandboxHelper(args []string, stderr io.Writer) (code int, isHelper bool) {
	if len(args) == 0 || args[0] != landlock.HelperFlag {
		return 0, false
	}
	if len(args) < 4 || args[2] != "--" {
		_, _ = fmt.Fprintln(stderr, "umbrald: usage: umbrald "+landlock.HelperFlag+" PROFILE -- COMMAND [ARG...]")
		return exitUsage, true
	}
	err := landlock.Exec(args[1], args[3:])
	_, _ = fmt.Fprintln(stderr, "umbrald:", err)
	return exitSandbox, true
}

// platformSandbox is this system's adapter of the sandbox port: `sandbox-exec` on macOS
// (T-HRD-16), which needs no helper, and Landlock everywhere else, where it enforces on Linux
// and says `unsupported_os` on the rest.
func platformSandbox(helper string) sandboxports.Sandbox {
	if runtime.GOOS == "darwin" {
		return seatbelt.New()
	}
	return landlock.New(helper)
}

// offSandbox is `[sandbox] mode = "off"`: nothing is confined, and the status says the user
// chose it (REQ-SBX-003).
type offSandbox struct{}

func (offSandbox) Status() sandboxdomain.Status {
	return sandboxdomain.Status{State: sandboxdomain.StateOff, Reason: sandboxdomain.ReasonConfig}
}

func (offSandbox) Command(_ sandboxdomain.Profile, argv []string) ([]string, error) { return argv, nil }

// codeCacheDir is where the libffi binding caches the library the daemon loads on macOS and
// Windows (T-HRD-18). No confined process may write it on any system: what lands there runs
// inside umbrald at its next start.
func codeCacheDir() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "github.com", "jupiterrider", "ffi")
}

// resolved follows the symbolic links of a path that exists and cleans one that does not, so
// every path in a profile is the one the kernel will see.
func resolved(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// newSandbox builds the sandbox of this system and the confiner of hosted agents, and checks
// `[sandbox] write` against the directories the daemon protects: its data and runtime
// directories, and the code cache. An entry that reaches one — itself or through a symbolic
// link — stops the daemon, like any other setting it cannot honour (REQ-SBX-004).
func newSandbox(settings config.Sandbox, dataDir, runtimeDir string, logger *slog.Logger) (sandboxports.Sandbox, acpConfiner, error) {
	protected := []string{resolved(dataDir), resolved(runtimeDir)}
	extra := make([]string, 0, len(settings.Write))
	for _, w := range settings.Write {
		extra = append(extra, resolved(w))
	}
	guarded := append([]string{resolved(codeCacheDir())}, protected...)
	if err := sandboxdomain.ValidateWrite(settings.Write, guarded); err != nil {
		return nil, acpConfiner{}, err
	}
	if err := sandboxdomain.ValidateWrite(extra, guarded); err != nil {
		return nil, acpConfiner{}, fmt.Errorf("%w (through a symbolic link)", err)
	}
	var sb sandboxports.Sandbox = offSandbox{}
	helper := ""
	if !settings.Off {
		exe, err := os.Executable()
		if err != nil {
			return nil, acpConfiner{}, fmt.Errorf("sandbox: find the daemon's own executable: %w", err)
		}
		helper = resolved(exe)
		sb = platformSandbox(helper)
	}
	st := sb.Status()
	if st.Enforcing() {
		logger.Info("sandbox ready", slog.String("mechanism", st.Mechanism))
	} else {
		// Degraded, and said (Art. 5, REQ-SBX-003): hosted agents run unconfined.
		logger.Warn("hosted agents run unconfined", slog.String("state", string(st.State)), slog.String("reason", st.Reason))
	}
	return sb, acpConfiner{
		sandbox: sb, protected: protected, extra: extra, helper: helper,
		worktrees: filepath.Join(protected[0], "worktrees"), homes: filepath.Join(protected[1], "acp"),
	}, nil
}

// sandboxStatus is the sandbox's state as system.status reports it.
func sandboxStatus(sb sandboxports.Sandbox) api.SandboxStatus {
	st := sb.Status()
	return api.SandboxStatus{State: string(st.State), Mechanism: st.Mechanism, Reason: st.Reason}
}

// acpConfiner makes each hosted agent's profile and asks the sandbox for the command that
// runs it confined (REQ-SBX-001, REQ-SBX-002). Every path it holds is resolved.
type acpConfiner struct {
	sandbox   sandboxports.Sandbox
	protected []string
	extra     []string
	// helper is the daemon's executable, which no confined agent may be able to replace.
	helper string
	// worktrees and homes are where the daemon makes a thread's worktree, under
	// `<repository>/<thread>`, and an agent's isolation root, under `<thread>`.
	worktrees, homes string
}

var _ acpports.Confiner = acpConfiner{}

// ConfinesNetwork reports whether a network profile would be enforced (REQ-SBX-008): the
// sandbox enforces, and its kernel has the rules for a network.
func (c acpConfiner) ConfinesNetwork() bool {
	st := c.sandbox.Status()
	return st.Enforcing() && st.Network
}

func (c acpConfiner) Confine(req acpdomain.Confinement, argv []string) ([]string, error) {
	if !c.sandbox.Status().Enforcing() {
		return argv, nil
	}
	dir, root := resolved(req.Dir), resolved(req.Root)
	if root != filepath.Join(c.homes, req.ThreadID) {
		return nil, fmt.Errorf("%w: its isolation root %s is not the one the daemon makes", sandboxdomain.ErrProfile, root)
	}
	agent := sandboxdomain.Agent{
		WorkDir: dir, Root: root, Extra: c.extra, Protected: c.protected,
		// The one directory inside Umbral's data a thread may work in: the worktree the
		// daemon made for it, `<worktrees>/<repository>/<thread>`.
		OwnWorktree: filepath.Dir(filepath.Dir(dir)) == c.worktrees && filepath.Base(dir) == req.ThreadID,
	}
	profile, err := sandboxdomain.AgentProfile(agent)
	if err != nil {
		return nil, err
	}
	if req.Net != nil {
		// Behind its relay: the relay's port and its own, nothing else (REQ-SBX-008). The
		// sandbox refuses a network it cannot enforce, and the agent is then not launched.
		profile.Net = &sandboxdomain.Net{Connect: req.Net.Connect, Bind: req.Net.Bind}
	}
	for _, w := range profile.Write {
		if sandboxdomain.Within(c.helper, w) {
			// A dev build inside the thread's directory: the agent could replace the binary
			// the next launch runs before it is confined.
			return nil, fmt.Errorf("%w: the daemon's executable %s is inside %s, which the agent may write", sandboxdomain.ErrProfile, c.helper, w)
		}
	}
	return c.sandbox.Command(profile, argv)
}
