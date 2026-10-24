package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const skillDigest = "9f2c0e7d"

func skillManifestResult() map[string]any {
	return map[string]any{
		"name": "release-check", "description": "Steps to cut a release.", "version": "1.2",
		"files":      []map[string]any{{"path": "SKILL.md", "size_bytes": 4100}, {"path": "scripts/check.sh", "size_bytes": 110}},
		"size_bytes": 4210, "sha256": skillDigest,
	}
}

func skillResult(enabled bool) map[string]any {
	return map[string]any{
		"id": "skl_1", "name": "release-check", "description": "Steps to cut a release.", "version": "1.2",
		"source": "/src/release-check", "sha256": skillDigest, "size_bytes": 4210, "enabled": enabled, "installed_at": 1,
	}
}

func skillDaemon(t *testing.T) (string, chan map[string]any) {
	t.Helper()
	seen := make(chan map[string]any, 16)
	socket := serveFakeDaemonWithParams(t, map[string]any{
		"skill.inspect": skillManifestResult(),
		"skill.install": skillResult(true),
	}, seen)
	return socket, seen
}

// installCalls drains what the fake daemon saw and keeps the skill.install params: the only
// request carrying expected_sha256.
func installCalls(seen chan map[string]any) (inspected, installed []map[string]any) {
	for len(seen) > 0 {
		p := <-seen
		if _, ok := p["expected_sha256"]; ok {
			installed = append(installed, p)
		} else {
			inspected = append(inspected, p)
		}
	}
	return inspected, installed
}

// TestInstallAsksOrNeedsYes_REQ_SKL_001: `umb skill install` shows the manifest and the
// digest, and installs only after a "y" on a terminal or with --yes. Without a terminal and
// without --yes it exits 1 and skill.install is never called. What it installs is the
// digest it showed, from an absolute path; a remote source goes to the daemon untouched,
// so the daemon's classification is the one that refuses it (REQ-SKL-006).
func TestInstallAsksOrNeedsYes_REQ_SKL_001(t *testing.T) {
	socket, seen := skillDaemon(t)
	src := t.TempDir()
	base := []string{"skill", "install", src, "--socket", socket, "--no-autostart"}

	var stdout, stderr bytes.Buffer
	if code := run(base, &stdout, &stderr); code != exitFailure {
		t.Fatalf("no terminal, no --yes: exit %d", code)
	}
	for _, want := range []string{"release-check", "1.2", skillDigest, "scripts/check.sh"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the manifest shown lacks %q: %q", want, stdout.String())
		}
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Errorf("stderr should say how to confirm: %q", stderr.String())
	}
	if inspected, installed := installCalls(seen); len(installed) != 0 || len(inspected) != 1 || inspected[0]["path"] != src {
		t.Fatalf("without confirmation: inspected %v, installed %v", inspected, installed)
	}

	// A terminal that answers no installs nothing; one that answers yes installs the digest shown.
	defer func(prev func(io.Reader) bool) { stdinIsTerminal = prev }(stdinIsTerminal)
	stdinIsTerminal = func(io.Reader) bool { return true }
	for answer, want := range map[string]int{"n\n": 0, "\n": 0, "": 0, "y": 1, "y\n": 1, "yes\n": 1} {
		stdout.Reset()
		stderr.Reset()
		code := runIO(base, strings.NewReader(answer), &stdout, &stderr)
		_, installed := installCalls(seen)
		if len(installed) != want {
			t.Errorf("answer %q: %d installs (exit %d)", answer, len(installed), code)
			continue
		}
		if want == 0 && code != exitFailure || want == 1 && code != exitOK {
			t.Errorf("answer %q: exit %d, stderr %q", answer, code, stderr.String())
		}
		if want == 1 && (installed[0]["expected_sha256"] != skillDigest || installed[0]["path"] != src) {
			t.Errorf("answer %q: install params %v", answer, installed[0])
		}
		if !strings.Contains(stderr.String(), "? [y/N]") || !strings.Contains(stderr.String(), skillDigest) {
			t.Errorf("answer %q: the prompt does not name the digest: %q", answer, stderr.String())
		}
	}

	// Reading the manifest takes the person longer than one call's deadline; the install's
	// deadline starts after the answer, so it still goes through.
	defer func(prev time.Duration) { skillCallTimeout = prev }(skillCallTimeout)
	skillCallTimeout = 200 * time.Millisecond
	stderr.Reset()
	if code := runIO(base, &slowReader{delay: 3 * skillCallTimeout, data: "y\n"}, &stdout, &stderr); code != exitOK {
		t.Errorf("a slow reader: exit %d, stderr %q", code, stderr.String())
	}
	if _, installed := installCalls(seen); len(installed) != 1 {
		t.Errorf("a slow reader: %d installs", len(installed))
	}
	stdinIsTerminal = func(io.Reader) bool { return false }

	// --yes without a terminal, --replace passed through, a relative path made absolute.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, src)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := run([]string{"skill", "install", rel, "--yes", "--replace", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--yes: exit %d, stderr %q", code, stderr.String())
	}
	inspected, installed := installCalls(seen)
	if len(installed) != 1 || installed[0]["replace"] != true || installed[0]["path"] != src || inspected[0]["path"] != src {
		t.Fatalf("--yes --replace with %q: inspected %v, installed %v", rel, inspected, installed)
	}
	if !strings.Contains(stdout.String(), "installed release-check") {
		t.Errorf("stdout %q", stdout.String())
	}

	// --json prints the installed Skill alone.
	stdout.Reset()
	if code := run([]string{"skill", "install", src, "--yes", "--json", "--socket", socket, "--no-autostart"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("--json: exit %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got["id"] != "skl_1" {
		t.Errorf("--json output %q: %v", stdout.String(), err)
	}
	installCalls(seen)

	// A remote source is sent as typed, never resolved against the working directory.
	stdout.Reset()
	remote := "git@github.com:someone/skill.git"
	run([]string{"skill", "install", remote, "--yes", "--socket", socket, "--no-autostart"}, &stdout, &stderr)
	if inspected, _ := installCalls(seen); len(inspected) != 1 || inspected[0]["path"] != remote {
		t.Errorf("a remote source reached the daemon as %v", inspected)
	}
}

// TestSkillCommandsMirrorTheMethods_REQ_SKL_003: list, show, enable, disable and remove call
// the skill.* method of the same name with API §5.42's params, print its result — JSON with
// --json — and exit as REQ-CLI-004 says, a daemon error being exit 1.
func TestSkillCommandsMirrorTheMethods_REQ_SKL_003(t *testing.T) {
	t.Parallel()
	seen := make(chan map[string]any, 16)
	socket := serveFakeDaemonWithParams(t, map[string]any{
		"skill.list":        map[string]any{"items": []any{skillResult(true)}},
		"skill.get":         map[string]any{"skill": skillResult(true), "body": "# Release\n\nRun the checklist.\n"},
		"skill.set_enabled": skillResult(false),
		"skill.remove": fakeError{
			"code": -32004, "message": "skill ghost is not installed",
			"data": map[string]any{"domain_code": "NOT_FOUND"},
		},
	}, seen)
	flags := []string{"--socket", socket, "--no-autostart"}
	call := func(args ...string) (int, string, string, map[string]any) {
		t.Helper()
		for len(seen) > 0 {
			<-seen
		}
		var stdout, stderr bytes.Buffer
		code := run(append(append([]string{"skill"}, args...), flags...), &stdout, &stderr)
		var params map[string]any
		if len(seen) > 0 {
			params = <-seen
		}
		return code, stdout.String(), stderr.String(), params
	}

	if code, out, _, _ := call("list"); code != exitOK || !strings.Contains(out, "release-check") || !strings.Contains(out, "enabled") {
		t.Errorf("list: exit %d, %q", code, out)
	}
	if code, out, _, _ := call("list", "--json"); code != exitOK || !strings.HasPrefix(strings.TrimSpace(out), `{"items":[`) {
		t.Errorf("list --json: exit %d, %q", code, out)
	}
	code, out, _, params := call("show", "release-check")
	if code != exitOK || params["name"] != "release-check" || params["include"] != nil || strings.Contains(out, "Run the checklist") {
		t.Errorf("show: exit %d, params %v, %q", code, params, out)
	}
	code, out, _, params = call("show", "release-check", "--body")
	if code != exitOK || params["include"] != "body" || !strings.Contains(out, "Run the checklist") {
		t.Errorf("show --body: exit %d, params %v, %q", code, params, out)
	}
	if code, out, _, _ := call("show", "release-check", "--json"); code != exitOK || !strings.Contains(out, `"skill":`) {
		t.Errorf("show --json: exit %d, %q", code, out)
	}
	for verb, enabled := range map[string]bool{"enable": true, "disable": false} {
		code, out, _, params := call(verb, "release-check")
		if code != exitOK || params["name"] != "release-check" || params["enabled"] != enabled || !strings.Contains(out, "release-check") {
			t.Errorf("%s: exit %d, params %v, %q", verb, code, params, out)
		}
	}
	code, out, errOut, params := call("remove", "ghost")
	if code != exitFailure || params["name"] != "ghost" || out != "" || !strings.Contains(errOut, "NOT_FOUND") && !strings.Contains(errOut, "not installed") {
		t.Errorf("remove of an unknown skill: exit %d, params %v, stdout %q, stderr %q", code, params, out, errOut)
	}
	for _, bad := range [][]string{{}, {"show"}, {"enable"}, {"remove", "a", "b"}, {"frobnicate", "x"}} {
		if code, _, _, _ := call(bad...); code != exitFailure {
			t.Errorf("umb skill %v: exit %d", bad, code)
		}
	}
}

// slowReader is a person reading the manifest before typing their answer.
type slowReader struct {
	delay time.Duration
	data  string
	done  bool
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.done = true
	return copy(p, r.data), nil
}

// TestTheManifestCannotHideAFile_REQ_SKL_001: the manifest is what the user confirms, and its
// text comes from the bundle. A path or description carrying a newline, a carriage return, an
// ESC sequence or a bidi override is printed escaped, so it can neither hide a file nor print
// a fake line.
func TestTheManifestCannotHideAFile_REQ_SKL_001(t *testing.T) {
	t.Parallel()
	hostile := skillManifestResult()
	hostile["description"] = "Steps.\r\x1b[2Kinnocent"
	hostile["files"] = []map[string]any{
		{"path": "SKILL.md", "size_bytes": 4100},
		{"path": "scripts/evil.sh\x1b[1A\x1b[2K", "size_bytes": 10},
		{"path": "notes\n      100  README.md", "size_bytes": 10},
		{"path": "doc\u202egnp.sh", "size_bytes": 10},
	}
	socket := serveFakeDaemon(t, map[string]any{"skill.inspect": hostile, "skill.list": map[string]any{"items": []any{
		map[string]any{"id": "skl_1", "name": "x", "description": "a\x1b]0;owned\x07", "version": "1\x1b[0m", "source": "/s", "sha256": "d", "size_bytes": 1, "enabled": true, "installed_at": 1},
	}}})
	for _, args := range [][]string{{"skill", "install", t.TempDir()}, {"skill", "list"}} {
		var stdout, stderr bytes.Buffer
		run(append(args, "--socket", socket, "--no-autostart"), &stdout, &stderr)
		out := stdout.String()
		for _, raw := range []string{"\x1b", "\r", "\u202e", "\x07"} {
			if strings.Contains(out, raw) {
				t.Errorf("%v printed a raw %q: %q", args, raw, out)
			}
		}
		if args[1] == "install" {
			if !strings.Contains(out, `scripts/evil.sh\x1b[1A`) || !strings.Contains(out, `notes\n      100  README.md`) {
				t.Errorf("the hostile paths are not shown escaped: %q", out)
			}
			if lines := strings.Count(out, "\n"); lines != 10 {
				t.Errorf("the manifest is %d lines, want 10 (6 lines of fields, 4 files): %q", lines, out)
			}
		}
	}
}

// TestOnlyACharacterDeviceIsATerminal_REQ_SKL_001: a pipe, a file or an in-memory reader is
// never taken for a terminal, so a piped "y" cannot confirm an install.
func TestOnlyACharacterDeviceIsATerminal_REQ_SKL_001(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	f, err := os.CreateTemp(t.TempDir(), "answer")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for name, in := range map[string]io.Reader{"a pipe": r, "a file": f, "a reader": strings.NewReader("y\n")} {
		if stdinIsTerminal(in) {
			t.Errorf("%s was taken for a terminal", name)
		}
	}
}
