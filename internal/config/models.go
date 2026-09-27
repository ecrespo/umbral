package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// ModelsFileName is the provider configuration beside config.toml (Tech Design §5.1,
// docs/ARCHITECTURE.md §7). It is a file of its own because it is the one a user edits most
// and shares least: it names their providers, never their keys.
const ModelsFileName = "models.toml"

// ModelsPath is the models file's full path.
func ModelsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ModelsFileName), nil
}

//go:embed models.schema.json
var modelsSchemaJSON []byte

// modelsSchema is compiled once; the document is part of the binary, so a failure here is a
// build defect and panics at init rather than at the first load.
var modelsSchema = func() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(modelsSchemaJSON))
	if err != nil {
		panic(fmt.Sprintf("config: the embedded models schema is not JSON: %v", err))
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("models.json", doc); err != nil {
		panic(fmt.Sprintf("config: the embedded models schema: %v", err))
	}
	s, err := c.Compile("models.json")
	if err != nil {
		panic(fmt.Sprintf("config: the embedded models schema does not compile: %v", err))
	}
	return s
}()

// Models is models.toml, validated.
type Models struct {
	Router    Router
	Classes   map[string][]string
	Providers []Provider
	// Rejected are provider entries refused one by one, a plaintext key above all
	// (REQ-SEC-004). The rest of the file still loads: refusing a user's local Ollama
	// because they pasted a key into another entry would punish the wrong provider.
	Rejected []Rejection
}

// Router is the `[router]` table.
type Router struct {
	// Policy is local-first, cost or quality; local-first by default.
	Policy string
	// Offline restricts candidates to local providers (REQ-LLM-004).
	Offline bool
	// MaxCostMicroUSDPerThread is `max_cost_usd_per_thread` converted at the boundary, since
	// money is int64 micro-USD everywhere past it (Art. 6). Zero means no ceiling.
	MaxCostMicroUSDPerThread int64
}

// Provider is one `[[providers]]` entry.
type Provider struct {
	ID         string
	Type       string
	BaseURL    string
	Credential CredentialRef
	Options    map[string]any
	LibPath    string
	ModelsDir  string
}

// CredentialKind is where a provider's key comes from.
type CredentialKind string

// The credential sources a models file may name. There is no "literal": a key written in
// the file is rejected (REQ-SEC-004).
const (
	CredentialNone    CredentialKind = "none"
	CredentialKeyring CredentialKind = "keyring"
	CredentialEnv     CredentialKind = "env"
)

// CredentialRef is a reference to a secret, never the secret: the keyring path or the
// environment variable name.
type CredentialRef struct {
	Kind CredentialKind
	Ref  string
}

// String renders the reference as it is written, which is safe to log and to show.
func (c CredentialRef) String() string {
	if c.Kind == CredentialNone {
		return ""
	}
	return string(c.Kind) + ":" + c.Ref
}

// Rejection is one refused provider entry: which, which field, and why, never the value.
type Rejection struct {
	ProviderID string
	Field      string
	Issue      string
}

// envName is a POSIX environment variable name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LoadModels reads and validates the models file. An absent file is no providers. A file the
// schema refuses is ErrSettingsInvalid naming why; a provider carrying a plaintext key is
// left out and listed in Rejected.
func LoadModels(path string) (Models, error) {
	models := Models{Router: Router{Policy: "local-first"}, Classes: map[string][]string{}}

	raw, err := os.ReadFile(path) // #nosec G304 -- the daemon's own configuration location
	if errors.Is(err, fs.ErrNotExist) {
		return models, nil
	}
	if err != nil {
		return models, fmt.Errorf("config: read %s: %w", path, err)
	}

	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return models, fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, path, err)
	}
	// Through JSON so the validator sees JSON's types rather than TOML's Go ones.
	asJSON, err := json.Marshal(doc)
	if err != nil {
		return models, fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, path, err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		return models, fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, path, err)
	}
	if err := modelsSchema.Validate(instance); err != nil {
		return models, fmt.Errorf("%w: %s: %s", ErrSettingsInvalid, path, schemaMessage(err))
	}

	var file struct {
		Router struct {
			Policy              string  `toml:"policy"`
			Offline             bool    `toml:"offline"`
			MaxCostUSDPerThread float64 `toml:"max_cost_usd_per_thread"`
		} `toml:"router"`
		Classes   map[string][]string `toml:"classes"`
		Providers []struct {
			ID        string         `toml:"id"`
			Type      string         `toml:"type"`
			BaseURL   string         `toml:"base_url"`
			APIKey    string         `toml:"api_key"`
			Options   map[string]any `toml:"options"`
			LibPath   string         `toml:"lib_path"`
			ModelsDir string         `toml:"models_dir"`
		} `toml:"providers"`
	}
	if err := toml.Unmarshal(raw, &file); err != nil {
		return models, fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, path, err)
	}

	if file.Router.Policy != "" {
		models.Router.Policy = file.Router.Policy
	}
	models.Router.Offline = file.Router.Offline
	models.Router.MaxCostMicroUSDPerThread = int64(math.Round(file.Router.MaxCostUSDPerThread * 1e6))
	for class, chain := range file.Classes {
		models.Classes[class] = chain
	}

	seen := map[string]bool{}
	for _, p := range file.Providers {
		if seen[p.ID] {
			return models, fmt.Errorf("%w: %s: two providers have the id %q", ErrSettingsInvalid, path, p.ID)
		}
		seen[p.ID] = true

		cred, rejection, err := parseCredential(p.ID, p.APIKey)
		if err != nil {
			return models, fmt.Errorf("%w: %s: %w", ErrSettingsInvalid, path, err)
		}
		if rejection == nil {
			rejection = secretOption(p.ID, p.Options)
		}
		if rejection != nil {
			models.Rejected = append(models.Rejected, *rejection)
			continue
		}
		models.Providers = append(models.Providers, Provider{
			ID: p.ID, Type: p.Type, BaseURL: p.BaseURL, Credential: cred,
			Options: p.Options, LibPath: p.LibPath, ModelsDir: p.ModelsDir,
		})
	}
	return models, nil
}

// parseCredential reads an api_key. A reference is parsed; a malformed reference is an error
// (the user meant a reference and mistyped it) that names the provider and the field but not
// the value, which may be the key itself pasted after the prefix; anything else is a key in the clear, which
// rejects the entry and names what to write instead, without repeating the value.
func parseCredential(providerID, value string) (CredentialRef, *Rejection, error) {
	switch {
	case value == "":
		return CredentialRef{Kind: CredentialNone}, nil, nil
	case strings.HasPrefix(value, "keyring:"):
		ref := strings.TrimPrefix(value, "keyring:")
		if strings.TrimSpace(ref) == "" || strings.ContainsAny(ref, " \t\n") {
			return CredentialRef{}, nil, fmt.Errorf("provider %q: api_key is not a keyring path; write keyring:<service>/<account>",
				providerID)
		}
		return CredentialRef{Kind: CredentialKeyring, Ref: ref}, nil, nil
	case strings.HasPrefix(value, "env:"):
		ref := strings.TrimPrefix(value, "env:")
		if !envName.MatchString(ref) {
			return CredentialRef{}, nil, fmt.Errorf("provider %q: api_key is not an environment variable name; write env:<VAR>",
				providerID)
		}
		return CredentialRef{Kind: CredentialEnv, Ref: ref}, nil, nil
	default:
		return CredentialRef{}, &Rejection{
			ProviderID: providerID,
			Field:      "api_key",
			Issue: "holds a key in plaintext; store it in the keyring and write " +
				`api_key = "keyring:<path>"`,
		}, nil
	}
}

// secretWords mark an option key that names a credential. api_key is the one place a
// credential goes, so a key like these under [providers.options] is a secret written where
// the plaintext check does not look, and is refused the same way (REQ-SEC-004).
var secretWords = []string{"key", "token", "secret", "password", "passwd", "auth", "credential", "bearer"}

// secretOption refuses the entry when an option's name says it holds a credential; like
// parseCredential it names the field and never the value. Options are provider tuning
// (num_ctx, keep_alive); nothing in them is sent as a header.
func secretOption(providerID string, options map[string]any) *Rejection {
	names := make([]string, 0, len(options))
	for name := range options {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lower := strings.ToLower(name)
		for _, word := range secretWords {
			if strings.Contains(lower, word) {
				return &Rejection{
					ProviderID: providerID,
					Field:      "options." + name,
					Issue: "looks like a credential; options never carry one: store it in the keyring and write " +
						`api_key = "keyring:<path>"`,
				}
			}
		}
	}
	return nil
}

// englishPrinter renders validation messages; the validator requires one.
var englishPrinter = message.NewPrinter(language.English)

// schemaMessage flattens a validation error into one line that names each failing location.
func schemaMessage(err error) string {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return err.Error()
	}
	var lines []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			lines = append(lines, "/"+strings.Join(e.InstanceLocation, "/")+": "+e.ErrorKind.LocalizedString(englishPrinter))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	sort.Strings(lines)
	return strings.Join(lines, "; ")
}
