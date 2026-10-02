// Package registry is the tools module's microkernel (Tech Design §2): every tool, built-in
// or MCP, registers here with a JSON Schema and a risk, and every call goes through here.
// Input is validated against its schema before the tool sees it, and a call runs only under
// a grant that allows it — the policy engine's decision, or the user's approval of its `ask`
// — so no tool runs outside security.Decide() (AGENTS.md, Art. 4).
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
	"github.com/ecrespo/umbral/internal/tools/domain"
	"github.com/ecrespo/umbral/internal/tools/ports"
)

// toolName is what a tool may be called: what model APIs accept, lower case.
var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type entry struct {
	tool   ports.Tool
	spec   domain.Spec
	schema *jsonschema.Schema
}

// Registry implements ports.Registry.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]entry
}

var _ ports.Registry = (*Registry)(nil)

// New builds a registry holding the given tools.
func New(tools ...ports.Tool) (*Registry, error) {
	r := &Registry{tools: map[string]entry{}}
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register adds a tool. Its name must be unique and well formed, its risk one of the four, its
// description present and its input schema a valid JSON Schema for an object.
func (r *Registry) Register(t ports.Tool) error {
	spec := t.Spec()
	if !toolName.MatchString(spec.Name) {
		return fmt.Errorf("registry: tool name %q is not [a-z][a-z0-9_]*", spec.Name)
	}
	switch spec.Risk {
	case secdomain.RiskReadOnly, secdomain.RiskWriteFS, secdomain.RiskExec, secdomain.RiskNetwork:
	default:
		return fmt.Errorf("registry: tool %s declares no risk class", spec.Name)
	}
	if strings.TrimSpace(spec.Description) == "" {
		return fmt.Errorf("registry: tool %s has no description", spec.Name)
	}
	if spec.InputSchema == nil || spec.InputSchema["type"] != "object" {
		return fmt.Errorf("registry: tool %s: its input schema must be an object schema", spec.Name)
	}
	schema, err := compile(spec)
	if err != nil {
		return fmt.Errorf("registry: tool %s: %w", spec.Name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[spec.Name]; dup {
		return fmt.Errorf("registry: tool %s is registered twice", spec.Name)
	}
	r.tools[spec.Name] = entry{tool: t, spec: spec, schema: schema}
	return nil
}

// Unregister removes a tool, and reports whether it was there. An MCP server's tools come and
// go with the server (REQ-MCP-002); a turn under way keeps the list it read when it started,
// and a call to a tool removed since is refused as an unknown tool.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.tools[name]
	delete(r.tools, name)
	return ok
}

func compile(spec domain.Spec) (*jsonschema.Schema, error) {
	raw, err := json.Marshal(spec.InputSchema)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	loc := "umbral:tool/" + spec.Name
	if err := c.AddResource(loc, doc); err != nil {
		return nil, err
	}
	return c.Compile(loc)
}

// Specs implements ports.Registry.
func (r *Registry) Specs() []domain.Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.Spec, 0, len(r.tools))
	for _, e := range r.tools {
		out = append(out, e.spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// checkEnv refuses an environment whose paths are not absolute.
func checkEnv(env domain.Env) error {
	if !filepath.IsAbs(env.Cwd) || (env.WriteRoot != "" && !filepath.IsAbs(env.WriteRoot)) {
		return fmt.Errorf("%w: cwd %q, write root %q", domain.ErrInvalidEnv, env.Cwd, env.WriteRoot)
	}
	return nil
}

// checked finds the call's tool and validates its input.
func (r *Registry) checked(call domain.Call) (entry, error) {
	r.mu.RLock()
	e, ok := r.tools[call.Tool]
	r.mu.RUnlock()
	if !ok {
		return entry{}, fmt.Errorf("%w: %s", domain.ErrUnknownTool, call.Tool)
	}
	input := call.Input
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("{}")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil {
		return entry{}, fmt.Errorf("%w: %s: the input is not JSON: %w", domain.ErrInvalidInput, call.Tool, err)
	}
	if err := e.schema.Validate(doc); err != nil {
		return entry{}, fmt.Errorf("%w: %s: %s", domain.ErrInvalidInput, call.Tool, message(err))
	}
	return e, nil
}

// message is a schema failure as one line the model can act on.
func message(err error) string {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return err.Error()
	}
	var leaves []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			where := "/" + strings.Join(e.InstanceLocation, "/")
			leaves = append(leaves, fmt.Sprintf("%s: %s", where, e.ErrorKind))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	return strings.Join(leaves, "; ")
}

func normalize(call domain.Call) json.RawMessage {
	if len(bytes.TrimSpace(call.Input)) == 0 {
		return json.RawMessage("{}")
	}
	return call.Input
}

// Action implements ports.Registry.
func (r *Registry) Action(env domain.Env, call domain.Call) (secdomain.Action, error) {
	if err := checkEnv(env); err != nil {
		return secdomain.Action{}, err
	}
	e, err := r.checked(call)
	if err != nil {
		return secdomain.Action{}, err
	}
	a, err := e.tool.Action(env, normalize(call))
	if err != nil {
		return secdomain.Action{}, err
	}
	// The risk the policy decides on is the one the tool declared, whatever Action says.
	a.Risk, a.Tool = e.spec.Risk, e.spec.Name
	return a, nil
}

// Summary is the call's one-line summary, or its tool's name when it has none.
func (r *Registry) Summary(call domain.Call) string {
	r.mu.RLock()
	e, ok := r.tools[call.Tool]
	r.mu.RUnlock()
	if !ok {
		return call.Tool
	}
	return e.tool.Summary(normalize(call))
}

// Preview implements ports.Registry.
func (r *Registry) Preview(ctx context.Context, env domain.Env, call domain.Call) (string, error) {
	if err := checkEnv(env); err != nil {
		return "", err
	}
	e, err := r.checked(call)
	if err != nil {
		return "", err
	}
	p, ok := e.tool.(ports.Previewer)
	if !ok {
		return "", nil
	}
	return p.Preview(ctx, env, normalize(call))
}

// Invoke implements ports.Registry. The input is validated first, so an invalid call is
// `invalid_args` whatever its grant. Then the call's action is computed again — its target
// resolved again through any symlinks — and must be the one the grant was decided on, and the
// grant must allow it.
func (r *Registry) Invoke(ctx context.Context, env domain.Env, call domain.Call, grant domain.Grant) (domain.Result, error) {
	a, err := r.Action(env, call)
	if err != nil {
		return domain.Result{}, err
	}
	if a != grant.Action {
		return domain.Result{}, fmt.Errorf("%w: %s: the grant was decided on %s %q, and the call now targets %q",
			domain.ErrNotAuthorized, call.Tool, grant.Action.Tool, grant.Action.Target, a.Target)
	}
	if !grant.Allows() {
		return domain.Result{}, fmt.Errorf("%w: %s (%s %s)", domain.ErrNotAuthorized, call.Tool, grant.Decision.Verdict, grant.Decision.Reason)
	}
	e, _ := r.checked(call)
	env.Target = a.Target
	return e.tool.Run(ctx, env, normalize(call))
}
