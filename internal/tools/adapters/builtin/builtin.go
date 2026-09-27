// Package builtin holds the tools Umbral offers the model without an MCP server
// (REQ-AGT-002): run_command, read_file, write_file, edit_file, grep, glob, list_dir and
// fetch_url.
package builtin

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	sessionsports "github.com/ecrespo/umbral/internal/sessions/ports"
	"github.com/ecrespo/umbral/internal/tools/domain"
	"github.com/ecrespo/umbral/internal/tools/ports"
)

// Config tunes the built-ins. The daemon sets the terminal and fetch_url's egress log and
// redaction rules; a tool missing what it needs refuses to run.
type Config struct {
	Fetch FetchConfig
	// Terminal runs run_command in a thread's PTY; without it run_command refuses to run.
	Terminal sessionsports.AgentTerminal
}

// All returns every built-in tool.
func All(cfg Config) []ports.Tool {
	return []ports.Tool{
		runCommand{term: cfg.Terminal},
		readFile{},
		writeFile{},
		editFile{},
		grepTool{},
		globTool{},
		listDir{},
		newFetchURL(cfg.Fetch),
	}
}

// resolve makes a path absolute against the call's working directory.
func resolve(env domain.Env, p string) string {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(env.Cwd, p)
	}
	return filepath.Clean(p)
}

// realPath is a path with every symlink resolved, which the policy's lexical write-root test
// needs (Tech §5.3): a link inside the root that points out of it must yield a target out of
// it. A path that does not exist yet resolves through its nearest existing ancestor, and an
// unresolvable one — a dangling link, a permission error — is returned as it was.
func realPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// target is a call's path as the policy sees it: absolute and with its links resolved.
func target(env domain.Env, p string) string { return realPath(resolve(env, p)) }

// decode reads a tool's input into its struct. The registry has already checked it against
// the schema, so an error here is a bug, reported as invalid input all the same.
func decode(input json.RawMessage, into any) error {
	if err := json.Unmarshal(input, into); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrInvalidInput, err)
	}
	return nil
}

// Schema keywords and the field names several tools share.
const (
	kType        = "type"
	kDescription = "description"
	fPath        = "path"
	fPattern     = "pattern"
)

// object builds an input schema: an object with these properties, these required, and no
// others.
func object(required []string, properties map[string]any) map[string]any {
	req := make([]any, len(required))
	for i, r := range required {
		req[i] = r
	}
	return map[string]any{
		kType: "object", "properties": properties, "required": req, "additionalProperties": false,
	}
}

func str(description string) map[string]any {
	return map[string]any{kType: "string", kDescription: description}
}

// nonEmpty is a string property that may not be empty.
func nonEmpty(description string) map[string]any {
	s := str(description)
	s["minLength"] = 1
	return s
}

func integer(description string, minimum int) map[string]any {
	return map[string]any{kType: "integer", "minimum": minimum, kDescription: description}
}
