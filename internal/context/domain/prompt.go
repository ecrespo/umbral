package domain

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"trim":   strings.TrimRight,
	"escape": escapeEntry,
}).ParseFS(templateFS, "templates/*.tmpl"))

// entryEscaper writes a catalog entry's ampersands and angle brackets as entities, so a skill's
// own text cannot close the <skills> block or open a tag the model would read as Umbral's.
var entryEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeEntry(s string) string { return entryEscaper.Replace(s) }

// SystemInput is what the system prompt is assembled from.
type SystemInput struct {
	Cwd  string
	Mode string
	// Rules are highest precedence first, as RulesCandidates orders them.
	Rules []RulesFile
	// Git is nil outside a repository.
	Git *GitContext
	// Skills is the catalog of enabled skills (REQ-SKL-004).
	Skills []CatalogEntry
}

// RenderSystem assembles the system prompt: the thread's cwd and mode, its rules files, the
// skill catalog and its git state (REQ-CTX-001, REQ-CTX-003, REQ-SKL-004).
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
