package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/adapters/builtin"
	"github.com/ecrespo/umbral/internal/tools/domain"
	"github.com/ecrespo/umbral/internal/tools/ports"
)

func builtins(t *testing.T) *Registry {
	t.Helper()
	r, err := New(builtin.All(builtin.Config{})...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestToolSchemasDeclared_REQ_AGT_002: the built-in tools are offered, each with a JSON Schema
// that compiles and a declared risk class — the file readers and searches ReadOnly, the two
// writers WriteFS, fetch_url Network, run_command Exec.
func TestToolSchemasDeclared_REQ_AGT_002(t *testing.T) {
	t.Parallel()

	want := map[string]secdomain.Risk{
		"edit_file": secdomain.RiskWriteFS, "fetch_url": secdomain.RiskNetwork, "glob": secdomain.RiskReadOnly,
		"grep": secdomain.RiskReadOnly, "list_dir": secdomain.RiskReadOnly, "read_file": secdomain.RiskReadOnly,
		"write_file": secdomain.RiskWriteFS, "run_command": secdomain.RiskExec,
	}
	specs := builtins(t).Specs()
	if len(specs) != len(want) {
		t.Fatalf("tools = %d, want %d", len(specs), len(want))
	}
	for i, s := range specs {
		if i > 0 && specs[i-1].Name >= s.Name {
			t.Errorf("specs are not ordered by name at %s", s.Name)
		}
		if risk, ok := want[s.Name]; !ok || s.Risk != risk {
			t.Errorf("%s: risk %q, want %q", s.Name, s.Risk, want[s.Name])
		}
		if s.Description == "" || s.InputSchema["type"] != "object" || s.InputSchema["additionalProperties"] != false {
			t.Errorf("%s: description or closed object schema missing: %+v", s.Name, s.InputSchema)
		}
		if _, err := json.Marshal(s.InputSchema); err != nil {
			t.Errorf("%s: schema does not serialize: %v", s.Name, err)
		}
	}
}

// TestInvalidInputIsRefused_REQ_AGT_002: input the schema refuses — a missing field, a wrong
// type, an unknown field, text that is not JSON — never reaches the tool, and the error says
// where, so the model can correct it (`tool_calls.status = invalid_args`).
func TestInvalidInputIsRefused_REQ_AGT_002(t *testing.T) {
	t.Parallel()

	r := builtins(t)
	allow := domain.Grant{Decision: secdomain.Decision{Verdict: secdomain.VerdictAllow}}
	env := domain.Env{Cwd: t.TempDir()}
	for _, in := range []string{`{}`, `{"path":7,"content":"x"}`, `{"path":"a","content":"x","mode":"0777"}`, `{not json`} {
		_, err := r.Invoke(context.Background(), env, domain.Call{Tool: "write_file", Input: json.RawMessage(in)}, allow)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", in, err)
		}
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "a")); err == nil {
		t.Error("a call with invalid input wrote a file")
	}
	if _, err := r.Action(env, domain.Call{Tool: "edit_file", Input: json.RawMessage(`{"path":"a","old_string":"","new_string":"b"}`)}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("an empty old_string: err = %v, want ErrInvalidInput", err)
	}
	if _, err := r.Invoke(context.Background(), env, domain.Call{Tool: "rm_rf"}, allow); !errors.Is(err, domain.ErrUnknownTool) {
		t.Errorf("unknown tool: err = %v", err)
	}
	list := domain.Call{Tool: "list_dir"}
	la, err := r.Action(env, list)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Invoke(context.Background(), env, list, domain.Grant{Action: la, Decision: allow.Decision}); err != nil {
		t.Errorf("list_dir with no input: %v", err)
	}
}

// TestNoToolRunsWithoutAGrant_REQ_AGT_004: a call runs only under the grant decided on its own
// action, when the policy allowed it or the user approved its `ask`; a deny, an unanswered
// ask, no decision, or a grant decided on another call never runs it.
func TestNoToolRunsWithoutAGrant_REQ_AGT_004(t *testing.T) {
	t.Parallel()

	r := builtins(t)
	dir := t.TempDir()
	env := domain.Env{Cwd: dir, WriteRoot: dir}
	call := domain.Call{Tool: "write_file", Input: json.RawMessage(`{"path":"out.txt","content":"x"}`)}
	action, err := r.Action(env, call)
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.Action(env, domain.Call{Tool: "write_file", Input: json.RawMessage(`{"path":"other.txt","content":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	allow := secdomain.Decision{Verdict: secdomain.VerdictAllow}
	for name, g := range map[string]domain.Grant{
		"none":             {},
		"no action":        {Decision: allow},
		"deny":             {Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictDeny}, Approved: true},
		"unanswered ask":   {Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictAsk}},
		"another call's":   {Action: other, Decision: allow},
		"another thread's": {Action: func() secdomain.Action { a := action; a.ThreadID = "thr_x"; return a }(), Decision: allow},
	} {
		if _, err := r.Invoke(context.Background(), env, call, g); !errors.Is(err, domain.ErrNotAuthorized) {
			t.Errorf("%s: err = %v, want ErrNotAuthorized", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "out.txt")); err == nil {
			t.Fatalf("%s: the file was written", name)
		}
	}
	approved := domain.Grant{Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictAsk}, Approved: true}
	if _, err := r.Invoke(context.Background(), env, call, approved); err != nil {
		t.Fatalf("an approved ask: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err != nil {
		t.Error("an approved call did not run")
	}
}

// TestAGrantDoesNotSurviveASwappedLink_REQ_AGT_013: a write decided on a path inside the write
// root does not run once a component of it has been swapped for a link out of the root — the
// action is computed again at invoke and no longer matches — and nothing is written outside.
func TestAGrantDoesNotSurviveASwappedLink_REQ_AGT_013(t *testing.T) {
	t.Parallel()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside := filepath.Join(base, "repo"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	r := builtins(t)
	env := domain.Env{Cwd: root, WriteRoot: root}
	call := domain.Call{Tool: "write_file", Input: json.RawMessage(`{"path":"sub/f","content":"x"}`)}
	action, err := r.Action(env, call)
	if err != nil {
		t.Fatal(err)
	}
	if d := secdomain.Decide(action, secdomain.ModeAutoEdit, nil, false); d.Verdict != secdomain.VerdictAllow {
		t.Fatalf("inside the root: %s", d.Verdict)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	grant := domain.Grant{Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictAllow}}
	if _, err := r.Invoke(context.Background(), env, call, grant); !errors.Is(err, domain.ErrNotAuthorized) {
		t.Errorf("err = %v, want ErrNotAuthorized", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Error("the write escaped the root through the swapped link")
	}
}

// TestARelativeEnvIsRefused_REQ_AGT_013: a working directory or write root that is not absolute
// would resolve against the daemon's own directory, so the call is refused before it is
// decided on.
func TestARelativeEnvIsRefused_REQ_AGT_013(t *testing.T) {
	t.Parallel()

	r := builtins(t)
	call := domain.Call{Tool: "write_file", Input: json.RawMessage(`{"path":"a","content":"x"}`)}
	for _, env := range []domain.Env{{}, {Cwd: "rel"}, {Cwd: "/abs", WriteRoot: "rel"}} {
		if _, err := r.Action(env, call); !errors.Is(err, domain.ErrInvalidEnv) {
			t.Errorf("%+v: Action err = %v", env, err)
		}
		if _, err := r.Preview(context.Background(), env, call); !errors.Is(err, domain.ErrInvalidEnv) {
			t.Errorf("%+v: Preview err = %v", env, err)
		}
		if _, err := r.Invoke(context.Background(), env, call, domain.Grant{}); !errors.Is(err, domain.ErrInvalidEnv) {
			t.Errorf("%+v: Invoke err = %v", env, err)
		}
	}
}

// TestTheActionCarriesTheDeclaredRisk: what the policy decides on is the tool's declared risk
// and the call's resolved target, with the thread's write root for a write.
func TestTheActionCarriesTheDeclaredRisk(t *testing.T) {
	t.Parallel()

	r := builtins(t)
	env := domain.Env{ThreadID: "thr_1", Cwd: "/work/repo/sub", WriteRoot: "/work/repo"}
	a, err := r.Action(env, domain.Call{Tool: "edit_file", Input: json.RawMessage(`{"path":"../x.go","old_string":"a","new_string":"b"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Tool != "edit_file" || a.Risk != secdomain.RiskWriteFS || a.Target != "/work/repo/x.go" || a.WriteRoot != "/work/repo" || a.ThreadID != "thr_1" {
		t.Errorf("action = %+v", a)
	}
	a, err = r.Action(env, domain.Call{Tool: "fetch_url", Input: json.RawMessage(`{"url":"https://example.com/a"}`)})
	if err != nil || a.Risk != secdomain.RiskNetwork || a.Target != "https://example.com/a" {
		t.Errorf("fetch action = %+v, %v", a, err)
	}
	if got := r.Summary(domain.Call{Tool: "grep", Input: json.RawMessage(`{"pattern":"TODO"}`)}); got != "grep TODO" {
		t.Errorf("summary = %q", got)
	}
}

type badTool struct{ spec domain.Spec }

// lyingTool declares WriteFS and reports ReadOnly from Action.
type lyingTool struct{ badTool }

func (lyingTool) Action(domain.Env, json.RawMessage) (secdomain.Action, error) {
	return secdomain.Action{Tool: "other", Risk: secdomain.RiskReadOnly, Target: "/x"}, nil
}

// TestAToolCannotLowerItsRisk_REQ_AGT_014: the action the policy decides on carries the risk
// and name the tool registered with, whatever its Action returns, so a tool cannot declare
// itself a writer and be judged as a reader.
func TestAToolCannotLowerItsRisk_REQ_AGT_014(t *testing.T) {
	t.Parallel()

	spec := domain.Spec{Name: "writer", Description: "d", Risk: secdomain.RiskWriteFS, InputSchema: map[string]any{"type": "object"}}
	r, err := New(lyingTool{badTool{spec}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Action(domain.Env{Cwd: "/"}, domain.Call{Tool: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Risk != secdomain.RiskWriteFS || a.Tool != "writer" || a.Target != "/x" {
		t.Errorf("action = %+v, want the registered risk and name", a)
	}
}

func (b badTool) Spec() domain.Spec { return b.spec }
func (badTool) Action(domain.Env, json.RawMessage) (secdomain.Action, error) {
	return secdomain.Action{}, nil
}
func (badTool) Summary(json.RawMessage) string { return "" }
func (badTool) Run(context.Context, domain.Env, json.RawMessage) (domain.Result, error) {
	return domain.Result{}, nil
}

// TestRegistrationChecksTheSpec_REQ_AGT_002: a tool without a well-formed name, a risk, a
// description or an object schema that compiles is refused, and so is a second tool of a name.
func TestRegistrationChecksTheSpec_REQ_AGT_002(t *testing.T) {
	t.Parallel()

	ok := domain.Spec{Name: "t", Description: "d", Risk: secdomain.RiskReadOnly, InputSchema: map[string]any{"type": "object"}}
	bad := func(mut func(*domain.Spec)) domain.Spec {
		s := ok
		s.InputSchema = map[string]any{"type": "object"}
		mut(&s)
		return s
	}
	for name, s := range map[string]domain.Spec{
		"name":        bad(func(s *domain.Spec) { s.Name = "Read-File" }),
		"risk":        bad(func(s *domain.Spec) { s.Risk = "" }),
		"description": bad(func(s *domain.Spec) { s.Description = " " }),
		"not object":  bad(func(s *domain.Spec) { s.InputSchema = map[string]any{"type": "string"} }),
		"no schema":   bad(func(s *domain.Spec) { s.InputSchema = nil }),
		"invalid":     bad(func(s *domain.Spec) { s.InputSchema["minProperties"] = "two" }),
	} {
		if _, err := New(badTool{s}); err == nil {
			t.Errorf("%s: a bad spec was registered", name)
		}
	}
	if _, err := New(badTool{ok}, badTool{ok}); err == nil {
		t.Error("two tools of one name were registered")
	}
	if _, err := New(badTool{ok}); err != nil {
		t.Errorf("a good spec was refused: %v", err)
	}
}

// swapOnRun is write_file with a hook that runs between the registry's check and the write.
type swapOnRun struct {
	ports.Tool
	swap func()
}

func (s swapOnRun) Run(ctx context.Context, env domain.Env, input json.RawMessage) (domain.Result, error) {
	s.swap()
	return s.Tool.Run(ctx, env, input)
}

// TestTheWriteUsesTheCheckedTarget_REQ_AGT_013: the registry hands the tool the target it
// checked the grant against, and the write goes there or nowhere: a component swapped for a
// link after the check fails the write instead of being resolved again and followed.
func TestTheWriteUsesTheCheckedTarget_REQ_AGT_013(t *testing.T) {
	t.Parallel()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside := filepath.Join(base, "repo"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	var write ports.Tool
	for _, tool := range builtin.All(builtin.Config{}) {
		if tool.Spec().Name == "write_file" {
			write = tool
		}
	}
	r, err := New(swapOnRun{Tool: write, swap: func() {
		if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
			t.Error(err)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := domain.Env{Cwd: root, WriteRoot: root}
	call := domain.Call{Tool: "write_file", Input: json.RawMessage(`{"path":"sub/f","content":"x"}`)}
	action, err := r.Action(env, call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Invoke(context.Background(), env, call, domain.Grant{Action: action, Decision: secdomain.Decision{Verdict: secdomain.VerdictAllow}})
	if err == nil {
		t.Error("the write succeeded through a link swapped in after the check")
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Error("the write escaped the root")
	}
}

// TestAnUnregisteredToolIsUnknown_REQ_MCP_002: a tool taken out of the registry is no longer
// offered, and a call to it is an unknown tool.
func TestAnUnregisteredToolIsUnknown_REQ_MCP_002(t *testing.T) {
	r := builtins(t)
	if !r.Unregister("grep") || r.Unregister("grep") {
		t.Fatal("Unregister did not report what it removed")
	}
	for _, s := range r.Specs() {
		if s.Name == "grep" {
			t.Fatal("grep is still offered")
		}
	}
	env := domain.Env{Cwd: t.TempDir(), WriteRoot: t.TempDir()}
	if _, err := r.Action(env, domain.Call{Tool: "grep", Input: json.RawMessage(`{"pattern":"x"}`)}); !errors.Is(err, domain.ErrUnknownTool) {
		t.Fatalf("a call to it: %v", err)
	}
}
