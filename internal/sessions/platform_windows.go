package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// pathSeparators are the characters that make a command name a path: Windows reads both.
const pathSeparators = `/\:`

// candidates is a name with each extension of the child's PATHEXT, which is how Windows finds
// `git` as `git.exe`; a name that already carries a runnable extension is tried as it is first.
func candidates(path string, env []string) []string {
	exts := pathExt(env)
	out := []string{}
	if hasExt(path, exts) {
		out = append(out, path)
	}
	for _, e := range exts {
		out = append(out, path+e)
	}
	return out
}

// runnable is an extension Windows runs, since it has no executable bit.
func runnable(path string, _ os.FileInfo) bool { return hasExt(path, pathExt(nil)) }

func hasExt(path string, exts []string) bool {
	ext := filepath.Ext(path)
	for _, e := range exts {
		if ext != "" && strings.EqualFold(ext, e) {
			return true
		}
	}
	return false
}

// pathExt is PATHEXT from env, or the daemon's, or Windows' own default.
func pathExt(env []string) []string {
	value := ""
	for _, entry := range env {
		if key, v, ok := strings.Cut(entry, "="); ok && envKeyIs(key, "PATHEXT") {
			value = v
		}
	}
	if value == "" {
		value = os.Getenv("PATHEXT")
	}
	if value == "" {
		value = ".COM;.EXE;.BAT;.CMD"
	}
	var out []string
	for _, e := range strings.Split(value, ";") {
		if e != "" {
			out = append(out, strings.ToLower(e))
		}
	}
	return out
}

// envKeyIs compares environment keys, which are case-insensitive on Windows: `Path` is PATH.
func envKeyIs(key, want string) bool { return strings.EqualFold(key, want) }

// defaultShell is $SHELL when the user set one, then PowerShell 7, the shell Umbral integrates
// on Windows (REQ-WIN-002), then %ComSpec% (delta `2026-10-windows-port`, decision 6).
func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		return pwsh
	}
	if comspec := os.Getenv("ComSpec"); comspec != "" {
		return comspec
	}
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
}
