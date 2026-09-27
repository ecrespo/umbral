package domain

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"trim": strings.TrimRight,
}).ParseFS(templateFS, "templates/*.tmpl"))

// SystemInput is what the system prompt is assembled from.
type SystemInput struct {
	Cwd  string
	Mode string
	// Rules are highest precedence first, as RulesCandidates orders them.
	Rules []RulesFile
	// Git is nil outside a repository.
	Git *GitContext
}

// RenderSystem assembles the system prompt: the thread's cwd and mode, its rules files and
// its git state (REQ-CTX-001, REQ-CTX-003).
func RenderSystem(in SystemInput) (string, error) {
	var s strings.Builder
	if err := templates.ExecuteTemplate(&s, "system.tmpl", in); err != nil {
		return "", err
	}
	return s.String(), nil
}

// UserInput is a user message and what it attaches.
type UserInput struct {
	Text        string
	Attachments []Attachment
}

// RenderUser assembles a user message with its attachments after the text (REQ-CTX-002).
func RenderUser(in UserInput) (string, error) {
	var s strings.Builder
	if err := templates.ExecuteTemplate(&s, "user.tmpl", in); err != nil {
		return "", err
	}
	// The template file's own final newline is not the user's.
	return strings.TrimSuffix(s.String(), "\n"), nil
}
