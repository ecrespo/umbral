// Package pluginstore is the plugin store of delta `2026-10-plugins`: a bundle inspected and
// installed by digest, copied into `plugins/` beside the database, with its MCP servers and
// skills added through their own modules and taken away with it (REQ-PLG-001, REQ-PLG-002).
// Nothing a plugin carries runs in the daemon: its manifest is parsed, its files are read.
package pluginstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/plugins/domain"
	"github.com/ecrespo/umbral/internal/plugins/ports"
	"github.com/ecrespo/umbral/internal/store"
)

const (
	// stagingPrefix marks a copy in flight; a plugin name cannot start with '.'.
	stagingPrefix = ".staging-"
	// ownerMarker makes a directory Umbral's plugin store: Recover deletes what no row
	// records, so it must never run over a directory a user keeps beside a moved database.
	ownerMarker = ".umbral-plugins"
)

// Config is what the store needs.
type Config struct {
	DB *store.Store
	// Dir is the plugins directory, `plugins/` beside the database.
	Dir       string
	Bundles   ports.Bundles
	Detection ports.Detection
	Servers   ports.Servers
	Skills    ports.Skills
	Bus       ports.Publisher
	NewID     func() string
	Now       func() time.Time
	Logger    *slog.Logger
}

// Service implements ports.Plugins.
type Service struct {
	cfg Config
	// mu serialises every change, so names and keys are checked and taken as one step.
	mu sync.Mutex
	// crashAfterServers stops an install whose contributions fail as a crash would, without
	// undoing anything: the state Data Model §6 step 10 recovers. Tests only.
	crashAfterServers bool
}

var _ ports.Plugins = (*Service)(nil)

// New builds the store and claims its directory.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.DB == nil:
		return nil, errors.New("plugins: a database is required")
	case cfg.Dir == "":
		return nil, errors.New("plugins: a directory is required")
	case cfg.Bundles == nil || cfg.Detection == nil || cfg.Servers == nil || cfg.Skills == nil:
		return nil, errors.New("plugins: the bundle reader, detection check, MCP servers and skills are required")
	case cfg.NewID == nil:
		return nil, errors.New("plugins: an id generator is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("plugins: %s: %w", cfg.Dir, err)
	}
	cfg.Dir = dir
	if err := claim(dir); err != nil {
		return nil, err
	}
	return &Service{cfg: cfg}, nil
}

// claim makes dir the store: created with the owner marker, or adopted when it exists empty.
func claim(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("plugins: create %s: %w", dir, err)
	}
	marker := filepath.Join(dir, ownerMarker)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("plugins: read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("plugins: %s is not Umbral's plugins directory (it holds files and no %s); move it, or put the database elsewhere", dir, ownerMarker)
	}
	if err := os.WriteFile(marker, []byte("Umbral's plugin store. Every directory here without a plugins row is removed at start.\n"), 0o600); err != nil {
		return fmt.Errorf("plugins: mark %s: %w", dir, err)
	}
	return nil
}

// Recover is Data Model §6 step 10. A row still `installing` is an install a crash stopped
// before its last contribution: what it added is undone and the row deleted. Then every
// staging directory and every copy no row records is removed, so a crash anywhere in an
// install leaves nothing behind.
func (s *Service) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unfinished, err := s.query(ctx, `WHERE state = 'installing'`)
	if err != nil {
		return err
	}
	for _, p := range unfinished {
		s.cfg.Logger.WarnContext(ctx, "undoing a plugin install a restart interrupted", slog.String("plugin", p.Name()))
		if err := s.drop(ctx, p); err != nil {
			return err
		}
	}
	names, err := s.names(ctx)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("plugins: read %s: %w", s.cfg.Dir, err)
	}
	for _, e := range entries {
		if e.Name() == ownerMarker || names[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.cfg.Dir, e.Name())); err != nil {
			return fmt.Errorf("plugins: remove the leftover %s: %w", e.Name(), err)
		}
	}
	return nil
}

// Inspect implements ports.Plugins.
func (s *Service) Inspect(ctx context.Context, path string) (domain.Inspection, error) {
	insp, _, err := s.read(ctx, path)
	return insp, err
}

// read checks the bundle at path and its manifest, each server as the MCP module would add it
// included, and returns both (decision 2).
func (s *Service) read(ctx context.Context, path string) (domain.Inspection, map[string][]byte, error) {
	files, err := s.cfg.Bundles.ReadFiles(path, domain.ManifestFile)
	if err != nil {
		return domain.Inspection{}, nil, fmt.Errorf("%w: %w", domain.ErrValidation, err)
	}
	m, err := parse(files[domain.ManifestFile])
	if err != nil {
		return domain.Inspection{}, nil, err
	}
	m, err = domain.Check(m, files, s.cfg.Detection.Check)
	if err != nil {
		return domain.Inspection{}, nil, err
	}
	for _, srv := range m.Servers {
		if err := s.cfg.Servers.Check(ctx, srv.AddParams()); err != nil {
			return domain.Inspection{}, nil, fmt.Errorf("%w: mcp server %q: %w", domain.ErrValidation, srv.Name, err)
		}
	}
	insp := domain.Inspection{Manifest: m, SHA256: ctxdomain.Digest(files)}
	for p, data := range files {
		insp.Files = append(insp.Files, domain.File{Path: p, Size: int64(len(data))})
		insp.SizeBytes += int64(len(data))
	}
	slices.SortFunc(insp.Files, func(a, b domain.File) int { return strings.Compare(a.Path, b.Path) })
	return insp, files, nil
}

// Install implements ports.Plugins (decisions 3 and 4).
func (s *Service) Install(ctx context.Context, path, expected string) (domain.Plugin, error) {
	insp, files, err := s.read(ctx, path)
	if err != nil {
		return domain.Plugin{}, err
	}
	if insp.SHA256 != expected {
		return domain.Plugin{}, fmt.Errorf("%w: the bundle changed after it was inspected (digest %s, expected %s); inspect it again",
			domain.ErrConflict, insp.SHA256, expected)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.free(ctx, insp.Manifest); err != nil {
		return domain.Plugin{}, err
	}

	p := domain.Plugin{
		ID: s.cfg.NewID(), Manifest: insp.Manifest, Source: path, SHA256: insp.SHA256,
		SizeBytes: insp.SizeBytes, InstalledAt: s.cfg.Now().UnixMilli(),
	}
	staging := filepath.Join(s.cfg.Dir, stagingPrefix+p.ID)
	defer func() { _ = os.RemoveAll(staging) }() // gone once renamed; removed if anything failed
	if err := writeFiles(staging, files); err != nil {
		return domain.Plugin{}, err
	}
	if err := s.insert(ctx, staging, p); err != nil {
		return domain.Plugin{}, err
	}
	added, err := s.contribute(ctx, p)
	if err == nil {
		err = s.finish(ctx, p)
	}
	if err != nil {
		if s.crashAfterServers {
			return domain.Plugin{}, err
		}
		if rmErr := s.undo(ctx, p, added); rmErr != nil {
			s.cfg.Logger.ErrorContext(ctx, "a failed plugin install could not be undone; the next start undoes it",
				slog.String("plugin", p.Name()), slog.Any("error", rmErr))
		}
		return domain.Plugin{}, err
	}
	s.publish(p.Name(), domain.ChangeInstalled)
	return p, nil
}

// contributions is what one install added, so a failure undoes that and nothing else: a server
// or skill another client added under the same name meanwhile is not this install's.
type contributions struct {
	servers []string
	skills  []string
}

// finish marks the row installed once every contribution is in: until then Recover undoes it.
func (s *Service) finish(ctx context.Context, p domain.Plugin) error {
	if _, err := s.cfg.DB.DB().ExecContext(ctx, `UPDATE plugins SET state = 'installed' WHERE id = ?`, p.ID); err != nil {
		return fmt.Errorf("plugins: mark %s installed: %w", p.Name(), err)
	}
	return nil
}

// undo removes what this install added, then its row and its copy.
func (s *Service) undo(ctx context.Context, p domain.Plugin, added contributions) error {
	var errs []error
	for _, name := range added.servers {
		if err := s.cfg.Servers.Remove(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("plugins: remove MCP server %s of %s: %w", name, p.Name(), err))
		}
	}
	for _, name := range added.skills {
		if err := s.cfg.Skills.Remove(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("plugins: remove skill %s of %s: %w", name, p.Name(), err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return s.deleteRow(ctx, p)
}

// free refuses, before anything is written, a name or key another plugin, server or skill has.
func (s *Service) free(ctx context.Context, m domain.Manifest) error {
	installed, err := s.list(ctx)
	if err != nil {
		return err
	}
	keys := map[string]string{}
	for _, p := range installed {
		if p.Name() == m.Name {
			return fmt.Errorf("%w: a plugin named %q is installed; remove it first", domain.ErrConflict, m.Name)
		}
		for _, k := range p.Manifest.Keys {
			keys[k.Key] = p.Name()
		}
	}
	for _, k := range m.Keys {
		if owner, ok := keys[k.Key]; ok {
			return fmt.Errorf("%w: plugin %s already binds %s", domain.ErrConflict, owner, k.Key)
		}
	}
	servers, err := s.cfg.Servers.List(ctx)
	if err != nil {
		return err
	}
	for _, srv := range servers {
		for _, want := range m.Servers {
			if srv.Name == want.Name {
				return fmt.Errorf("%w: an MCP server named %q exists", domain.ErrConflict, want.Name)
			}
		}
	}
	skills, err := s.cfg.Skills.List(ctx)
	if err != nil {
		return err
	}
	for _, sk := range skills {
		for _, want := range m.Skills {
			if sk.Name == want.Name {
				return fmt.Errorf("%w: a skill named %q is installed", domain.ErrConflict, want.Name)
			}
		}
	}
	return nil
}

// contribute adds the plugin's servers, untrusted, and installs its skills from the stored
// copy, so what is installed is what was inspected. It returns what it added, failed or not.
func (s *Service) contribute(ctx context.Context, p domain.Plugin) (contributions, error) {
	var added contributions
	for _, srv := range p.Manifest.Servers {
		if _, err := s.cfg.Servers.Add(ctx, srv.AddParams()); err != nil {
			return added, fmt.Errorf("plugins: add MCP server %s of %s: %w", srv.Name, p.Name(), err)
		}
		added.servers = append(added.servers, srv.Name)
	}
	for _, sk := range p.Manifest.Skills {
		dir := filepath.Join(s.copyOf(p.Name()), filepath.FromSlash(sk.Path))
		if _, err := s.cfg.Skills.Install(ctx, ctxdomain.InstallParams{Path: dir, ExpectedSHA256: sk.SHA256}); err != nil {
			return added, fmt.Errorf("plugins: install skill %s of %s: %w", sk.Name, p.Name(), err)
		}
		added.skills = append(added.skills, sk.Name)
	}
	return added, nil
}

// insert records the plugin and renames its staged copy into place inside the transaction: a
// crash between the rename and the commit leaves a copy without a row, which Recover removes.
func (s *Service) insert(ctx context.Context, staging string, p domain.Plugin) error {
	manifestJSON, err := json.Marshal(p.Manifest)
	if err != nil {
		return fmt.Errorf("plugins: encode the manifest of %s: %w", p.Name(), err)
	}
	tx, err := s.cfg.DB.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("plugins: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO plugins(id, name, version, description, source, sha256, size_bytes, manifest_json, installed_at, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'installing')`,
		p.ID, p.Name(), p.Manifest.Version, p.Manifest.Description, p.Source, p.SHA256, p.SizeBytes,
		string(manifestJSON), p.InstalledAt); err != nil {
		return fmt.Errorf("plugins: record %s: %w", p.Name(), err)
	}
	// No row records this name, so a directory under it is a leftover.
	if err := os.RemoveAll(s.copyOf(p.Name())); err != nil {
		return fmt.Errorf("plugins: clear a leftover %s: %w", p.Name(), err)
	}
	if err := os.Rename(staging, s.copyOf(p.Name())); err != nil {
		return fmt.Errorf("plugins: move %s into place: %w", p.Name(), err)
	}
	if err := tx.Commit(); err != nil {
		_ = os.RemoveAll(s.copyOf(p.Name()))
		return fmt.Errorf("plugins: commit %s: %w", p.Name(), err)
	}
	return nil
}

// List implements ports.Plugins: the installed plugins, never one an install is still adding.
func (s *Service) List(ctx context.Context) ([]domain.Plugin, error) {
	return s.query(ctx, `WHERE state = 'installed'`)
}

// list is every row, an unfinished install included: its name and keys are taken.
func (s *Service) list(ctx context.Context) ([]domain.Plugin, error) {
	return s.query(ctx, "")
}

func (s *Service) query(ctx context.Context, where string) ([]domain.Plugin, error) {
	rows, err := s.cfg.DB.DB().QueryContext(ctx,
		`SELECT id, source, sha256, size_bytes, manifest_json, installed_at FROM plugins `+where+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("plugins: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Plugin
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get implements ports.Plugins.
func (s *Service) Get(ctx context.Context, name string) (domain.Plugin, error) {
	p, err := scan(s.cfg.DB.DB().QueryRowContext(ctx,
		`SELECT id, source, sha256, size_bytes, manifest_json, installed_at FROM plugins WHERE name = ? AND state = 'installed'`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Plugin{}, fmt.Errorf("%w: %s", domain.ErrNotFound, name)
	}
	return p, err
}

// Remove implements ports.Plugins (decision 9).
func (s *Service) Remove(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if err := s.drop(ctx, p); err != nil {
		return err
	}
	s.publish(name, domain.ChangeRemoved)
	return nil
}

// drop removes what the plugin's manifest declares and is still the plugin's, then its row and
// its copy (decision 9). A server is still the plugin's while it is untrusted: one the user made
// trusted, by removing it and adding it again, is the user's. A skill is still the plugin's
// while it holds the digest the plugin installed.
func (s *Service) drop(ctx context.Context, p domain.Plugin) error {
	servers, err := s.cfg.Servers.List(ctx)
	if err != nil {
		return err
	}
	for _, srv := range p.Manifest.Servers {
		if slices.ContainsFunc(servers, func(have mcpdomain.Server) bool {
			return have.Name == srv.Name && have.Trust == mcpdomain.TrustUntrusted
		}) {
			if err := s.cfg.Servers.Remove(ctx, srv.Name); err != nil {
				return fmt.Errorf("plugins: remove MCP server %s of %s: %w", srv.Name, p.Name(), err)
			}
		}
	}
	skills, err := s.cfg.Skills.List(ctx)
	if err != nil {
		return err
	}
	for _, sk := range p.Manifest.Skills {
		if slices.ContainsFunc(skills, func(have ctxdomain.Skill) bool { return have.Name == sk.Name && have.SHA256 == sk.SHA256 }) {
			if err := s.cfg.Skills.Remove(ctx, sk.Name); err != nil {
				return fmt.Errorf("plugins: remove skill %s of %s: %w", sk.Name, p.Name(), err)
			}
		}
	}
	return s.deleteRow(ctx, p)
}

// deleteRow deletes the plugin's row, then its copy.
func (s *Service) deleteRow(ctx context.Context, p domain.Plugin) error {
	if _, err := s.cfg.DB.DB().ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, p.ID); err != nil {
		return fmt.Errorf("plugins: delete %s: %w", p.Name(), err)
	}
	// The row is gone, so a copy left behind here is one Recover removes at the next start.
	if err := os.RemoveAll(s.copyOf(p.Name())); err != nil {
		s.cfg.Logger.WarnContext(ctx, "a removed plugin's copy stays until the next start", slog.String("plugin", p.Name()), slog.Any("error", err))
	}
	return nil
}

// DetectionFiles is every installed plugin's detection manifests, by plugin name then by the
// manifest's order, read at the daemon's start (decision 7).
func (s *Service) DetectionFiles(ctx context.Context) ([]string, error) {
	plugins, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range plugins {
		for _, d := range p.Manifest.Detection {
			out = append(out, filepath.Join(s.copyOf(p.Name()), filepath.FromSlash(d.Path)))
		}
	}
	return out, nil
}

func (s *Service) copyOf(name string) string { return filepath.Join(s.cfg.Dir, name) }

func (s *Service) publish(name, change string) {
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(ports.PluginChanged{Name: name, Change: change})
	}
}

func (s *Service) names(ctx context.Context) (map[string]bool, error) {
	plugins, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(plugins))
	for _, p := range plugins {
		out[p.Name()] = true
	}
	return out, nil
}

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (domain.Plugin, error) {
	var p domain.Plugin
	var manifestJSON string
	if err := r.Scan(&p.ID, &p.Source, &p.SHA256, &p.SizeBytes, &manifestJSON, &p.InstalledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Plugin{}, err
		}
		return domain.Plugin{}, fmt.Errorf("plugins: read a row: %w", err)
	}
	if err := json.Unmarshal([]byte(manifestJSON), &p.Manifest); err != nil {
		return domain.Plugin{}, fmt.Errorf("plugins: decode the manifest of %s: %w", p.ID, err)
	}
	return p, nil
}

// writeFiles writes a bundle's files under dir, modes forced to 0644 and 0755: what is copied
// is the files' content, as the digest is.
func writeFiles(dir string, files map[string][]byte) error {
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // #nosec G301 -- inside the owner-only plugins directory
			return fmt.Errorf("plugins: stage %s: %w", name, err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil { // #nosec G306 -- inside the owner-only plugins directory
			return fmt.Errorf("plugins: stage %s: %w", name, err)
		}
	}
	return nil
}

// tomlManifest is `umbral-plugin.toml`'s shape. Unknown keys refuse the file, so a `trust` on
// a server, or a misspelled key, is reported rather than ignored (decisions 2 and 5).
type tomlManifest struct {
	Name        string `toml:"name"`
	Version     string `toml:"version"`
	Description string `toml:"description"`
	Commands    []struct {
		Name        string   `toml:"name"`
		Description string   `toml:"description"`
		Argv        []string `toml:"argv"`
	} `toml:"commands"`
	Servers []struct {
		Name      string            `toml:"name"`
		Transport string            `toml:"transport"`
		Command   string            `toml:"command"`
		Args      []string          `toml:"args"`
		URL       string            `toml:"url"`
		EnvRefs   map[string]string `toml:"env_refs"`
	} `toml:"mcp_servers"`
	Skills []struct {
		Path string `toml:"path"`
	} `toml:"skills"`
	Detection []struct {
		Path string `toml:"path"`
	} `toml:"detection"`
	Keys []struct {
		Key     string `toml:"key"`
		Command string `toml:"command"`
	} `toml:"keys"`
}

// parse reads a manifest. Neither go-toml's rendering of a syntax error nor a type mismatch's
// message is passed on: the first quotes the file's lines, the second names Go internals.
func parse(raw []byte) (domain.Manifest, error) {
	var f tomlManifest
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			keys := make([]string, 0, len(strict.Errors))
			for i := range strict.Errors {
				line, _ := strict.Errors[i].Position()
				keys = append(keys, fmt.Sprintf("unknown key %q at line %d", strings.Join(strict.Errors[i].Key(), "."), line))
			}
			return domain.Manifest{}, fmt.Errorf("%w: %s: %s", domain.ErrValidation, domain.ManifestFile, strings.Join(keys, "; "))
		}
		var decode *toml.DecodeError
		if errors.As(err, &decode) {
			line, _ := decode.Position()
			return domain.Manifest{}, fmt.Errorf("%w: %s is not valid TOML at line %d", domain.ErrValidation, domain.ManifestFile, line)
		}
		return domain.Manifest{}, fmt.Errorf("%w: %s: a field has the wrong type", domain.ErrValidation, domain.ManifestFile)
	}
	m := domain.Manifest{Name: f.Name, Version: f.Version, Description: f.Description}
	for _, c := range f.Commands {
		m.Commands = append(m.Commands, domain.Command{Name: c.Name, Description: c.Description, Argv: c.Argv})
	}
	for _, srv := range f.Servers {
		m.Servers = append(m.Servers, domain.Server{
			Name: srv.Name, Transport: srv.Transport, Command: srv.Command, Args: srv.Args, URL: srv.URL, EnvRefs: srv.EnvRefs,
		})
	}
	for _, sk := range f.Skills {
		m.Skills = append(m.Skills, domain.Skill{Path: sk.Path})
	}
	for _, d := range f.Detection {
		m.Detection = append(m.Detection, domain.Detection{Path: d.Path})
	}
	for _, k := range f.Keys {
		m.Keys = append(m.Keys, domain.Key{Key: k.Key, Command: k.Command})
	}
	return m, nil
}
