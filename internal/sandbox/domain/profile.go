// Package domain holds the sandbox's pure types: the profile a confined process runs under,
// and how one is made for a hosted agent (delta `2026-11-sandbox-linux`).
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Mode is `[sandbox] mode`.
type Mode string

const (
	// ModeEnforce confines what the sandbox covers wherever the system can; the default.
	ModeEnforce Mode = "enforce"
	// ModeOff is the disclosed opt-out (REQ-SBX-003).
	ModeOff Mode = "off"
)

// State is what `system.status` reports of the sandbox.
type State string

const (
	// StateEnforcing is when a process the sandbox covers is confined by the kernel.
	StateEnforcing State = "enforcing"
	// StateUnavailable is when this system cannot confine; Reason says why.
	StateUnavailable State = "unavailable"
	// StateOff is `[sandbox] mode = "off"`.
	StateOff State = "off"
)

// Reasons a sandbox is not enforcing.
const (
	ReasonUnsupportedOS = "unsupported_os"
	ReasonNoLandlock    = "landlock_unavailable"
	// ReasonOldLandlock is a kernel whose Landlock does not mediate truncate (before 6.2).
	ReasonOldLandlock = "landlock_too_old"
	// ReasonUnsupportedArch is a CPU architecture the socket filter is not written for.
	ReasonUnsupportedArch = "unsupported_arch"
	// ReasonNoSeatbelt is a macOS where `sandbox-exec` is missing or cannot apply a profile,
	// as inside another sandbox.
	ReasonNoSeatbelt = "seatbelt_unavailable"
	// ReasonNoHelper is a sandbox given no helper to run.
	ReasonNoHelper = "no_helper"
	ReasonConfig   = "config"
)

// Status is the sandbox's state on this system.
type Status struct {
	State State
	// Mechanism names what enforces it: `landlock`. Empty unless enforcing.
	Mechanism string
	// Reason says why it is not enforcing, and is empty when it is.
	Reason string
}

// Enforcing reports whether a covered process is confined.
func (s Status) Enforcing() bool { return s.State == StateEnforcing }

// Profile is what a confined process may touch. Everything the user can read stays readable
// except DenyRead; nothing is writable except Write and Devices.
type Profile struct {
	// Write are the directories and files the process may write, create in and remove from.
	Write []string `json:"write"`
	// DenyRead are directories the process may not read or list, save for a Write entry
	// inside one.
	DenyRead []string `json:"deny_read"`
}

// Devices are the device files every confined process may write: a redirect to /dev/null, and
// its own controlling terminal. Without them `git` and every shell fail at their first
// redirect (docs/spikes/2026-11-landlock.md). The pseudo-terminals are not among them: with
// `/dev/pts` a process could open the user's other terminals, Umbral's panes included.
var Devices = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/tty"}

// Validate refuses a profile the adapter could not apply as written.
func (p Profile) Validate() error {
	for _, list := range [][]string{p.Write, p.DenyRead} {
		for _, path := range list {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return fmt.Errorf("sandbox: %q is not a clean absolute path", path)
			}
		}
	}
	return nil
}

// Encode is the profile as the helper's argument.
func (p Profile) Encode() (string, error) {
	b, err := json.Marshal(p)
	return string(b), err
}

// Decode reads a profile the helper was given, and validates it.
func Decode(arg string) (Profile, error) {
	var p Profile
	dec := json.NewDecoder(strings.NewReader(arg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("sandbox: the profile does not parse: %w", err)
	}
	return p, p.Validate()
}

// Within reports whether path is dir or inside it.
func Within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// reaches reports whether path is, holds or lies inside one of dirs.
func reaches(path string, dirs []string) bool {
	for _, d := range dirs {
		if d != "" && (Within(path, d) || Within(d, path)) {
			return true
		}
	}
	return false
}

// ErrWrite is a `[sandbox] write` entry the daemon refuses.
var ErrWrite = errors.New("sandbox: write entry refused")

// ValidateWrite checks `[sandbox] write`: each entry a clean absolute path other than the
// root, and none that is, holds or lies inside a protected directory — Umbral's own data,
// and a directory the daemon loads code from (REQ-SBX-004).
func ValidateWrite(entries, protected []string) error {
	for _, e := range entries {
		switch {
		case !filepath.IsAbs(e) || filepath.Clean(e) != e:
			return fmt.Errorf("%w: %q is not a clean absolute path", ErrWrite, e)
		case e == string(filepath.Separator):
			return fmt.Errorf("%w: %q is the whole file system", ErrWrite, e)
		}
		if reaches(e, protected) {
			return fmt.Errorf("%w: %q reaches a directory no confined process may write", ErrWrite, e)
		}
	}
	return nil
}

// Agent is what a hosted agent's profile is made from. Every path is resolved: no symbolic
// link in it.
type Agent struct {
	// WorkDir is the thread's worktree, or its directory without one.
	WorkDir string
	// OwnWorktree says WorkDir is the worktree the daemon made for this thread, which is the
	// one directory inside Umbral's data a thread may work in.
	OwnWorktree bool
	// Root is the agent's isolation root, which holds its home and its temporary directory;
	// the daemon made it, inside its runtime directory.
	Root string
	// Extra is `[sandbox] write`.
	Extra []string
	// Protected are Umbral's data and runtime directories.
	Protected []string
}

// ErrProfile is an agent no profile can confine: launching it would open what the sandbox is
// there to close.
var ErrProfile = errors.New("sandbox: the agent cannot be confined")

// AgentProfile is a hosted agent's profile (REQ-SBX-001, REQ-SBX-002): it writes its thread's
// directory, its own isolation root and `[sandbox] write`, and reads everything but Umbral's
// directories — where the other threads' worktrees and the other agents' homes live.
//
// Nothing of the repository's `.git` is written, a worktree's own record there included: the
// daemon's git reads that record, the objects and the refs when it lands the thread's work,
// and an agent that could write them could change what lands after the review, or what
// commands that git runs. A confined agent therefore cannot commit; what lands is its
// worktree's files, which Umbral snapshots itself.
//
// A rule on a directory covers everything beneath it, so a thread whose directory holds one of
// Umbral's — the user's home, the root — cannot be confined, and neither can one working
// inside Umbral's data unless that is its own worktree.
func AgentProfile(a Agent) (Profile, error) {
	for _, p := range a.Protected {
		switch {
		case p == "":
		case Within(p, a.WorkDir):
			return Profile{}, fmt.Errorf("%w: its directory %s holds %s", ErrProfile, a.WorkDir, p)
		case Within(a.WorkDir, p) && !a.OwnWorktree:
			return Profile{}, fmt.Errorf("%w: its directory %s is inside %s and is not its worktree", ErrProfile, a.WorkDir, p)
		}
	}
	write := make([]string, 0, 2+len(a.Extra))
	write = append(write, a.WorkDir, a.Root)
	write = append(write, a.Extra...)
	return Profile{Write: write, DenyRead: append([]string(nil), a.Protected...)}, nil
}
