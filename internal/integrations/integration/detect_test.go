package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/integrations"
	"github.com/ecrespo/umbral/internal/integrations/adapters/manifests"
	"github.com/ecrespo/umbral/internal/integrations/domain"
	"github.com/ecrespo/umbral/internal/integrations/ports"
	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
	wsports "github.com/ecrespo/umbral/internal/workspaces/ports"
)

// fakeProcesses is a process table the test writes: each session's root pid, what that root
// runs as, and what runs under it.
type fakeProcesses struct {
	mu     sync.Mutex
	shells map[string]int
	roots  map[int][]string
	under  map[int][]domain.Process
}

func newFakeProcesses() *fakeProcesses {
	return &fakeProcesses{shells: map[string]int{}, roots: map[int][]string{}, under: map[int][]domain.Process{}}
}

func (f *fakeProcesses) ShellPID(sessionID string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pid, ok := f.shells[sessionID]
	return pid, ok
}

// Snapshot is the table itself: the test changes it only between scans.
func (f *fakeProcesses) Snapshot() ports.ProcessSnapshot { return f }

func (f *fakeProcesses) Subtree(pid int) []domain.Process {
	f.mu.Lock()
	defer f.mu.Unlock()
	root := f.roots[pid]
	if root == nil {
		root = []string{"/bin/sh"}
	}
	return append([]domain.Process{{PID: pid, Argv: root}}, f.under[pid]...)
}

// execAs makes argv the session's root process itself: a pane launched with a command, or a
// shell that ran `exec`.
func (f *fakeProcesses) execAs(sessionID string, root int, argv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shells[sessionID] = root
	f.roots[root] = argv
}

// run puts argv under the session's shell, replacing whatever ran there.
func (f *fakeProcesses) run(sessionID string, shell int, argv ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shells[sessionID] = shell
	if len(argv) == 0 {
		delete(f.under, shell)
		return
	}
	f.under[shell] = []domain.Process{{PID: shell + 1, Argv: argv}}
}

func (h *harness) detector(t *testing.T, procs *fakeProcesses, ms []domain.Manifest, skipped []domain.SkippedManifest) *integrations.Detector {
	t.Helper()
	d, err := integrations.NewDetector(integrations.DetectorConfig{
		Manifests: ms, Skipped: skipped, Labels: h.labels, Shells: procs, Processes: procs,
		Panes: h.tree, Bus: h.bus,
	})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	return d
}

func paneUpdates(events []bus.Event) []wsdomain.Pane {
	var out []wsdomain.Pane
	for _, ev := range events {
		if e, ok := ev.(wsports.PaneEvent); ok && e.Kind == wsports.KindPaneUpdated {
			out = append(out, e.Pane)
		}
	}
	return out
}

// TestAPaneRunningOpenCodeIsLabelled_REQ_XAG_001: a pane whose shell runs OpenCode is labelled
// with the agent and the state source its manifest declares, the change is announced as
// `pane.updated`, and the label goes when the agent exits.
func TestAPaneRunningOpenCodeIsLabelled_REQ_XAG_001(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pane := h.newPane(t).RootPane
	procs := newFakeProcesses()
	d := h.detector(t, procs, domain.Builtins(), nil)

	procs.run(pane.SessionID, 100, "/usr/local/bin/opencode")
	if err := d.Scan(t.Context()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := h.pane(t, pane.ID).DetectedAgent
	if got == nil || got.Name != "opencode" || got.StateSource != domain.AgentStateACP {
		t.Fatalf("detected_agent = %+v, want opencode over acp", got)
	}
	updates := paneUpdates(h.drain())
	if len(updates) != 1 || updates[0].DetectedAgent == nil || updates[0].DetectedAgent.Name != "opencode" {
		t.Fatalf("announced %+v, want one pane.updated carrying the label", updates)
	}
	if err := d.Scan(t.Context()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if again := paneUpdates(h.drain()); len(again) != 0 {
		t.Errorf("an unchanged label was announced again: %+v", again)
	}

	procs.run(pane.SessionID, 100)
	if err := d.Scan(t.Context()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := h.pane(t, pane.ID).DetectedAgent; got != nil {
		t.Errorf("detected_agent = %+v after the agent exited", got)
	}
	if gone := paneUpdates(h.drain()); len(gone) != 1 || gone[0].DetectedAgent != nil {
		t.Errorf("the label's removal was announced as %+v", gone)
	}
}

// TestDetectionNeverSetsState_REQ_XAG_001: detecting an agent changes neither the pane's state
// nor who decides it — not even on a pane no source decides, where detection becoming the
// lowest layer of DD-012 would show — announces no `pane.state_changed`, and an external report
// still decides over a detected pane.
func TestDetectionNeverSetsState_REQ_XAG_001(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	shell := h.newPane(t).RootPane
	silent := h.newPane(t).RootPane
	// No shell integration, so nothing decides this pane's state.
	h.exec(t, `UPDATE sessions SET integration = 'none' WHERE id = ?`, silent.SessionID)
	procs := newFakeProcesses()
	d := h.detector(t, procs, domain.Builtins(), nil)

	for _, pane := range []wsdomain.Pane{shell, silent} {
		before := h.pane(t, pane.ID)
		procs.run(pane.SessionID, 100+len(pane.ID), "claude", "--resume")
		if err := d.Scan(t.Context()); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		after := h.pane(t, pane.ID)
		if after.DetectedAgent == nil || after.DetectedAgent.Name != "claude-code" {
			t.Fatalf("%s: detected_agent = %+v", pane.ID, after.DetectedAgent)
		}
		if after.AttentionState != before.AttentionState || after.StateSource != before.StateSource {
			t.Errorf("%s: state %s from %q, was %s from %q", pane.ID, after.AttentionState, after.StateSource, before.AttentionState, before.StateSource)
		}
	}
	if got := h.pane(t, silent.ID); got.AttentionState != wsdomain.AttentionUnknown || got.StateSource != "" {
		t.Errorf("an undecided detected pane reads %s from %q, want unknown from no source", got.AttentionState, got.StateSource)
	}
	if changes := stateChanges(h.drain()); len(changes) != 0 {
		t.Errorf("detection announced state changes: %+v", changes)
	}

	one := int64(1)
	if err := h.ReportState(t.Context(), domain.Report{
		PaneID: silent.ID, Source: "claude-code", Agent: "claude", State: domain.StateWorking, Seq: &one,
	}); err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	if err := d.Scan(t.Context()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := h.pane(t, silent.ID); got.AttentionState != wsdomain.AttentionWorking || got.StateSource != "claude-code" {
		t.Errorf("after a report the pane reads %s from %q, want working from the reporter", got.AttentionState, got.StateSource)
	}
}

// TestAnAgentThatIsThePanesRootIsLabelled_REQ_XAG_001: a pane launched with the agent as its
// command, or whose shell ran `exec agent`, has no shell above the agent; it is labelled all the
// same, while a plain shell as the root matches nothing.
func TestAnAgentThatIsThePanesRootIsLabelled_REQ_XAG_001(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pane := h.newPane(t).RootPane
	procs := newFakeProcesses()
	d := h.detector(t, procs, domain.Builtins(), nil)

	procs.run(pane.SessionID, 200)
	if err := d.Scan(t.Context()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := h.pane(t, pane.ID).DetectedAgent; got != nil {
		t.Fatalf("a bare shell was labelled %+v", got)
	}
	procs.execAs(pane.SessionID, 200, "/usr/local/bin/opencode")
	if err := d.Scan(t.Context()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := h.pane(t, pane.ID).DetectedAgent; got == nil || got.Name != "opencode" {
		t.Errorf("detected_agent = %+v, want opencode as the pane's root", got)
	}
}

// TestAgentExplainNamesTheRule_REQ_XAG_003: agent.explain names the manifest, where it came
// from, the rule and the process it matched — by pid and executable, never its arguments —
// and who decides the pane's state.
func TestAgentExplainNamesTheRule_REQ_XAG_003(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pane := h.newPane(t).RootPane
	procs := newFakeProcesses()
	d := h.detector(t, procs, domain.Builtins(), nil)
	procs.run(pane.SessionID, 200, "/usr/bin/node", "/usr/lib/node_modules/@google/gemini-cli/dist/index.js", "--api-key", "s3cret")

	ex, err := d.Explain(t.Context(), pane.ID)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	det := ex.Detection
	if det == nil || det.Manifest.Name != "gemini-cli" || det.Manifest.Origin != domain.OriginBuiltin ||
		det.Rule != domain.RuleArgv || det.PID != 201 || det.Executable != "node" {
		t.Fatalf("detection %+v", det)
	}
	if det.Pattern != det.Manifest.Argv[0] {
		t.Errorf("pattern %q is not the manifest's", det.Pattern)
	}
	current := h.pane(t, pane.ID)
	if ex.AttentionState != string(current.AttentionState) || ex.StateSource != current.StateSource || ex.StateSource != wsdomain.SourceShell {
		t.Errorf("state authority %s/%q, the pane says %s/%q", ex.AttentionState, ex.StateSource, current.AttentionState, current.StateSource)
	}
	// Explain scans afresh: the label is there without a periodic scan having run.
	if current.DetectedAgent == nil || current.DetectedAgent.Name != "gemini-cli" {
		t.Errorf("detected_agent = %+v after explain", current.DetectedAgent)
	}

	idle := h.newPane(t).RootPane
	ex, err = d.Explain(t.Context(), idle.ID)
	if err != nil || ex.Detection != nil {
		t.Errorf("a pane running nothing: %+v, %v", ex, err)
	}
	if _, err := d.Explain(t.Context(), "w99:p1"); err == nil {
		t.Error("an unknown pane was explained")
	}
}

// TestALocalManifestOverridesByName_REQ_XAG_002: a local manifest named like a built-in one
// replaces it — the built-in rule no longer matches and the local one does — and explain says
// it came from that file.
func TestALocalManifestOverridesByName_REQ_XAG_002(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifest(t, dir, "goose.toml", `
name = "goose"
state_source = "hooks"
[detect]
executables = ["goose-wrapper"]
`)
	local, skipped := manifests.Load(dir)
	if len(skipped) != 0 || len(local) != 1 {
		t.Fatalf("loaded %+v, skipped %+v", local, skipped)
	}
	h := newHarness(t)
	pane := h.newPane(t).RootPane
	procs := newFakeProcesses()
	d := h.detector(t, procs, domain.Overlay(domain.Builtins(), local), skipped)

	procs.run(pane.SessionID, 300, "goose")
	if ex, _ := d.Explain(t.Context(), pane.ID); ex.Detection != nil {
		t.Errorf("the built-in goose rule still matched: %+v", ex.Detection)
	}
	procs.run(pane.SessionID, 300, "/opt/bin/goose-wrapper")
	ex, err := d.Explain(t.Context(), pane.ID)
	if err != nil || ex.Detection == nil {
		t.Fatalf("explain: %+v, %v", ex, err)
	}
	if ex.Detection.Manifest.Origin != filepath.Join(dir, "goose.toml") || ex.Detection.Manifest.StateSource != domain.AgentStateHooks {
		t.Errorf("manifest %+v", ex.Detection.Manifest)
	}
}

// TestAnInvalidManifestIsSkippedAndReported_REQ_XAG_002: a file with an unknown key, a bad
// pattern or bad TOML is skipped whole, the valid files beside it load, and every skipped file
// is listed with its reason in agent.explain.
func TestAnInvalidManifestIsSkippedAndReported_REQ_XAG_002(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Valid but for one misspelled key: without the strict check it would load and match.
	writeManifest(t, dir, "a-typo.toml", "name = \"aider-x\"\nstate_source = \"none\"\n[detect]\nexecutables = [\"aider\"]\nargv_pattern = [\"--api-key=s3cr3t\"]\n")
	writeManifest(t, dir, "b-pattern.toml", "name = \"codex\"\nstate_source = \"none\"\n[detect]\nargv = [\"--api-key=s3cr3t(\"]\n")
	writeManifest(t, dir, "c-syntax.toml", "name = \"x\n")
	writeManifest(t, dir, "c-type.toml", "name = 42\nstate_source = \"s3cr3t\"\n[detect]\nexecutables = [\"s3cr3t/x\"]\n")
	writeManifest(t, dir, "c-values.toml", "name = \"s3cr3t X\"\nstate_source = \"none\"\n[detect]\nexecutables = [\"x\"]\n")
	writeManifest(t, dir, "d-good.toml", "name = \"aider\"\nstate_source = \"none\"\n[detect]\nexecutables = [\"aider\"]\n")
	writeManifest(t, dir, "notes.txt", "not a manifest")

	local, skipped := manifests.Load(dir)
	if len(local) != 1 || local[0].Name != "aider" {
		t.Fatalf("loaded %+v", local)
	}
	if len(skipped) != 5 {
		t.Fatalf("skipped %+v, want the five invalid files", skipped)
	}
	for _, s := range skipped {
		if s.Reason == "" || filepath.Dir(s.File) != dir {
			t.Errorf("skipped %+v", s)
		}
		// A reason is logged and answered, so it names what is wrong and quotes no value.
		if strings.Contains(s.Reason, "s3cr3t") {
			t.Errorf("%s: the reason quotes the file: %q", s.File, s.Reason)
		}
	}
	if typo := skipped[0].Reason; !strings.Contains(typo, `"detect.argv_pattern"`) || !strings.Contains(typo, "line 5") {
		t.Errorf("the typo's reason %q does not name the key and its line", typo)
	}

	h := newHarness(t)
	pane := h.newPane(t).RootPane
	procs := newFakeProcesses()
	d := h.detector(t, procs, domain.Overlay(domain.Builtins(), local), skipped)
	procs.run(pane.SessionID, 400, "codex")
	ex, err := d.Explain(t.Context(), pane.ID)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(ex.Skipped) != 5 {
		t.Errorf("explain reports %d skipped files", len(ex.Skipped))
	}
	// The invalid codex override was skipped whole, so the built-in codex still decides.
	if ex.Detection == nil || ex.Detection.Manifest.Origin != domain.OriginBuiltin {
		t.Errorf("detection %+v", ex.Detection)
	}
}

func writeManifest(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
