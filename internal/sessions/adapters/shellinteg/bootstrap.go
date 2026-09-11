// Package shellinteg injects Umbral's shell-integration bootstrap into a new session
// (REQ-BLK-005, DD-002).
//
// Blocks are derived from OSC sequences the shell emits, not from guessing where a prompt
// begins, because prompt heuristics break on Starship and powerlevel10k. Each shell needs
// a different injection mechanism, and each one must leave the user's own configuration
// loaded: a terminal that silently drops someone's rc file is worse than one with no
// integration.
package shellinteg

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/shell"
)

// ErrUnsupportedShell reports a shell Umbral has no bootstrap for. The caller starts the
// session anyway; REQ-BLK-003 then marks it `integration: none` after five seconds.
var ErrUnsupportedShell = errors.New("shellinteg: unsupported shell")

// Kind is a supported shell.
type Kind string

// The shells Umbral ships a bootstrap for. PowerShell is REQ-BLK-008, deferred to F2
// with Windows.
const (
	Bash Kind = "bash"
	Zsh  Kind = "zsh"
	Fish Kind = "fish"
)

// DetectKind identifies the shell from its executable path. It matches on the base name,
// so /bin/bash, /usr/bin/bash and a versioned /opt/homebrew/bin/bash all resolve.
func DetectKind(shellPath string) (Kind, error) {
	base := filepath.Base(shellPath)
	switch {
	case strings.HasPrefix(base, "bash"):
		return Bash, nil
	case strings.HasPrefix(base, "zsh"):
		return Zsh, nil
	case strings.HasPrefix(base, "fish"):
		return Fish, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedShell, base)
	}
}

// Bootstrap is how to launch one shell with the integration injected.
type Bootstrap struct {
	// Kind is the shell that was detected.
	Kind Kind
	// Args are appended to the shell's argv.
	Args []string
	// Env are KEY=VALUE entries to add to the child's environment.
	Env []string
	// dir holds the materialised scripts and is removed by Close.
	dir string
}

// Argv assembles the shell's argument list, putting the bootstrap's own arguments first.
//
// The order is not cosmetic. bash's usage is "bash [GNU long option] [option] ...", and
// `bash -i --init-file F` exits 2 with "--: invalid option" while `bash --init-file F -i`
// works. A caller that appended the bootstrap after its own flags would get a shell that
// refuses to start, so the assembly lives here rather than in every caller.
func (b *Bootstrap) Argv(extra ...string) []string {
	argv := make([]string, 0, len(b.Args)+len(extra))
	argv = append(argv, b.Args...)
	return append(argv, extra...)
}

// Close removes the temporary directory holding the scripts. It is safe to call more than
// once, and safe to call while the shell is still running: every shell reads its startup
// file once, at startup.
func (b *Bootstrap) Close() error {
	if b.dir == "" {
		return nil
	}
	dir := b.dir
	b.dir = ""
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("shellinteg: remove %s: %w", dir, err)
	}
	return nil
}

// Prepare materialises the bootstrap for the given shell and returns how to launch it.
//
// The caller owns the result and must Close it once the shell has exited.
//
// Each shell gets the only injection mechanism that both runs before the first prompt and
// leaves the user's configuration loaded:
//
//   - bash: --init-file, which replaces ~/.bashrc, so the script sources it back. The
//     user's original path travels in UMBRAL_BASH_RC for the case where HOME is not where
//     the rc lives.
//   - zsh: a temporary ZDOTDIR holding a .zshrc, which is the only hook that runs before
//     the user's own .zshrc. The real ZDOTDIR travels in UMBRAL_ZDOTDIR and the script
//     restores it before sourcing anything.
//   - fish: --init-command, which runs after config.fish rather than instead of it, so
//     nothing needs restoring.
func Prepare(shellPath string) (*Bootstrap, error) {
	kind, err := DetectKind(shellPath)
	if err != nil {
		return nil, err
	}

	// 0700: the directory holds a file the shell will execute.
	dir, err := os.MkdirTemp("", "umbral-shellinteg-")
	if err != nil {
		return nil, fmt.Errorf("shellinteg: create the bootstrap directory: %w", err)
	}
	b := &Bootstrap{Kind: kind, dir: dir}

	switch kind {
	case Bash:
		path, err := b.materialise("bash/umbral.bash", "umbral.bash")
		if err != nil {
			return nil, err
		}
		b.Args = []string{"--init-file", path}
		if rc := userBashRC(); rc != "" {
			b.Env = []string{"UMBRAL_BASH_RC=" + rc}
		}

	case Zsh:
		if _, err := b.materialise("zsh/.zshrc", ".zshrc"); err != nil {
			return nil, err
		}
		b.Env = []string{"ZDOTDIR=" + dir}
		if original, ok := os.LookupEnv("ZDOTDIR"); ok {
			b.Env = append(b.Env, "UMBRAL_ZDOTDIR="+original)
		}

	case Fish:
		path, err := b.materialise("fish/umbral.fish", "umbral.fish")
		if err != nil {
			return nil, err
		}
		b.Args = []string{"--init-command", "source " + path}
	}

	return b, nil
}

// materialise copies one embedded script into the bootstrap directory.
func (b *Bootstrap) materialise(embedded, name string) (string, error) {
	data, err := fs.ReadFile(shell.FS, embedded)
	if err != nil {
		_ = b.Close()
		return "", fmt.Errorf("shellinteg: read the embedded %s: %w", embedded, err)
	}

	path := filepath.Join(b.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = b.Close()
		return "", fmt.Errorf("shellinteg: write %s: %w", path, err)
	}
	return path, nil
}

// userBashRC reports the rc file the bootstrap should source back. It is only consulted
// when HOME would give the wrong answer, so an unset HOME yields an empty string and the
// script falls back to ~/.bashrc itself.
func userBashRC() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	rc := filepath.Join(home, ".bashrc")
	if _, err := os.Stat(rc); err != nil {
		return ""
	}
	return rc
}

// Adapter implements ports.Bootstrapper by preparing a Bootstrap and flattening it into
// the shape the sessions service wants.
type Adapter struct{}

// Prepare satisfies ports.Bootstrapper.
func (Adapter) Prepare(shellPath string) (args, env []string, cleanup func() error, err error) {
	b, err := Prepare(shellPath)
	if err != nil {
		return nil, nil, nil, err
	}
	return b.Argv(), b.Env, b.Close, nil
}

// Compile-time proof that the adapter satisfies the port.
var _ ports.Bootstrapper = Adapter{}
