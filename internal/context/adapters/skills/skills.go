// Package skills is the skill store of delta `2026-09-skills-cli`: bundles checked, copied
// into `$XDG_DATA_HOME/umbral/skills/<name>/` through a staging directory and a rename, and
// recorded in the `skills` table (Data Model §2.4f). Nothing in a skill is executed here.
package skills

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/context/domain"
	"github.com/ecrespo/umbral/internal/context/ports"
	"github.com/ecrespo/umbral/internal/store"
)

// The names that mark a directory as in flight rather than installed. A skill name cannot
// start with '.', so neither can collide with one.
const (
	stagingPrefix = ".staging-"
	oldPrefix     = ".old-"
	// ownerMarker is the file that makes a directory Umbral's skill store. Recover deletes
	// what no row records, so it must never run over a directory a user keeps beside a
	// database moved with --db (decision 2).
	ownerMarker = ".umbral-skills"
)

// Config is what the store needs.
type Config struct {
	DB *store.Store
	// Dir is the skills directory, `skills/` beside the database.
	Dir    string
	Bus    *bus.Bus
	NewID  func() string
	Now    func() time.Time
	Logger *slog.Logger
}

// Service implements ports.Skills.
type Service struct {
	cfg Config
	// mu serialises every change, so a name is never installed twice at once and the
	// 64-enabled limit is read and written as one step.
	mu sync.Mutex
}

var _ ports.Skills = (*Service)(nil)

// New builds the store and creates its directory.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.DB == nil:
		return nil, errors.New("skills: a database is required")
	case cfg.Dir == "":
		return nil, errors.New("skills: a directory is required")
	case cfg.NewID == nil:
		return nil, errors.New("skills: an id generator is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if err := claim(cfg.Dir); err != nil {
		return nil, err
	}
	return &Service{cfg: cfg}, nil
}

// claim makes dir the store: created with the owner marker, or adopted when it exists empty.
// A directory holding anything without the marker is someone else's, and is refused rather
// than swept.
func claim(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("skills: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory: owner-only, with the search bit it needs
		return fmt.Errorf("skills: restrict %s: %w", dir, err)
	}
	marker := filepath.Join(dir, ownerMarker)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("skills: read %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("skills: %s is not Umbral's skills directory (it holds files and no %s); move it, or put the database elsewhere", dir, ownerMarker)
	}
	if err := os.WriteFile(marker, []byte("Umbral's skill store. Every directory here without a skills row is removed at start.\n"), 0o600); err != nil {
		return fmt.Errorf("skills: mark %s: %w", dir, err)
	}
	return nil
}

// Recover is Data Model §6 step 9: it removes every staging directory and every directory no
// row records, and settles a replace interrupted between its renames — the old copy comes
// back if the new one never arrived, and is dropped if it did.
func (s *Service) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.names(ctx)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("skills: read %s: %w", s.cfg.Dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if after, ok := strings.CutPrefix(name, oldPrefix); ok && names[after] != "" {
			if err := s.settleReplace(ctx, after, names[after]); err != nil {
				return err
			}
			continue
		}
		// Staging and set-aside copies start with '.', which no skill name can, so a single
		// check covers them with every directory no row records.
		if names[name] == "" {
			if err := os.RemoveAll(s.path(name)); err != nil {
				return fmt.Errorf("skills: remove %s: %w", name, err)
			}
			s.cfg.Logger.InfoContext(ctx, "removed a skill directory no record holds", slog.String("directory", name))
		}
	}
	return nil
}

// settleReplace resolves a replace cut short: the copy the row records stays, whichever of
// the two directories holds it. A `<name>` whose digest is not the row's is a new copy that
// moved in before the row was updated, and goes.
func (s *Service) settleReplace(ctx context.Context, name, recorded string) error {
	current, old := s.path(name), s.path(oldPrefix+name)
	if _, err := os.Stat(current); err == nil {
		if b, readErr := ReadBundle(current); readErr == nil && b.SHA256 == recorded {
			return os.RemoveAll(old)
		}
		if err := os.RemoveAll(current); err != nil {
			return fmt.Errorf("skills: remove the unrecorded copy of %s: %w", name, err)
		}
	}
	if err := os.Rename(old, current); err != nil {
		return fmt.Errorf("skills: restore %s: %w", name, err)
	}
	s.cfg.Logger.InfoContext(ctx, "restored the recorded copy of a skill after an interrupted replace", slog.String("skill", name))
	return nil
}

// Inspect implements ports.Skills.
func (s *Service) Inspect(_ context.Context, path string) (domain.Manifest, error) {
	b, err := ReadBundle(path)
	return b.Manifest, err
}

// Install implements ports.Skills.
func (s *Service) Install(ctx context.Context, p domain.InstallParams) (domain.Skill, error) {
	b, err := ReadBundle(p.Path)
	if err != nil {
		return domain.Skill{}, err
	}
	if b.SHA256 != p.ExpectedSHA256 {
		return domain.Skill{}, fmt.Errorf("%w: the bundle changed after it was inspected (digest %s, expected %s); inspect it again",
			domain.ErrSkillConflict, b.SHA256, p.ExpectedSHA256)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.get(ctx, s.cfg.DB.DB(), b.Name)
	exists := err == nil
	if err != nil && !errors.Is(err, domain.ErrSkillNotFound) {
		return domain.Skill{}, err
	}
	if exists && !p.Replace {
		return domain.Skill{}, fmt.Errorf("%w: a skill named %q is installed; pass replace to overwrite it", domain.ErrSkillConflict, b.Name)
	}

	sk := domain.Skill{
		ID: s.cfg.NewID(), Name: b.Name, Description: b.Description, Version: b.Version, Source: p.Path,
		SHA256: b.SHA256, SizeBytes: b.SizeBytes, Enabled: true, InstalledAt: s.cfg.Now().UnixMilli(),
	}
	staging := s.path(stagingPrefix + sk.ID)
	if err := writeBundle(staging, b); err != nil {
		_ = os.RemoveAll(staging)
		return domain.Skill{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }() // gone once renamed; removed if anything failed

	if exists {
		sk.ID, sk.Enabled = existing.ID, existing.Enabled
		err = s.replace(ctx, staging, sk)
	} else {
		err = s.insert(ctx, staging, &sk)
	}
	if err != nil {
		return domain.Skill{}, err
	}
	change := domain.SkillInstalled
	if exists {
		change = domain.SkillReplaced
	}
	s.publish(sk.Name, change)
	return sk, nil
}

// insert records a new skill and renames its staged copy into place inside the transaction:
// a crash between the rename and the commit leaves a directory without a row, which Recover
// removes.
func (s *Service) insert(ctx context.Context, staging string, sk *domain.Skill) error {
	tx, err := s.cfg.DB.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("skills: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var enabled int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM skills WHERE enabled = 1`).Scan(&enabled); err != nil {
		return fmt.Errorf("skills: count the enabled skills: %w", err)
	}
	sk.Enabled = enabled < domain.MaxEnabledSkills
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO skills(id, name, description, version, source, sha256, size_bytes, enabled, installed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sk.ID, sk.Name, sk.Description, nullable(sk.Version), sk.Source, sk.SHA256, sk.SizeBytes, sk.Enabled, sk.InstalledAt); err != nil {
		return fmt.Errorf("skills: record %s: %w", sk.Name, err)
	}
	// No row records this name, so a directory under it is a leftover a failed remove logged.
	if err := os.RemoveAll(s.path(sk.Name)); err != nil {
		return fmt.Errorf("skills: clear a leftover %s: %w", sk.Name, err)
	}
	if err := os.Rename(staging, s.path(sk.Name)); err != nil {
		return fmt.Errorf("skills: move %s into place: %w", sk.Name, err)
	}
	if err := tx.Commit(); err != nil {
		_ = os.RemoveAll(s.path(sk.Name))
		return fmt.Errorf("skills: commit %s: %w", sk.Name, err)
	}
	return nil
}

// replace sets the old copy aside, renames the new one in, updates the row and then drops
// the old copy. Recover settles a crash at any point between.
func (s *Service) replace(ctx context.Context, staging string, sk domain.Skill) error {
	current, old := s.path(sk.Name), s.path(oldPrefix+sk.Name)
	if err := os.Rename(current, old); err != nil {
		return fmt.Errorf("skills: set %s aside: %w", sk.Name, err)
	}
	if err := os.Rename(staging, current); err != nil {
		_ = os.Rename(old, current)
		return fmt.Errorf("skills: move %s into place: %w", sk.Name, err)
	}
	if _, err := s.cfg.DB.DB().ExecContext(ctx,
		`UPDATE skills SET description = ?, version = ?, source = ?, sha256 = ?, size_bytes = ?, installed_at = ? WHERE id = ?`,
		sk.Description, nullable(sk.Version), sk.Source, sk.SHA256, sk.SizeBytes, sk.InstalledAt, sk.ID); err != nil {
		_ = os.RemoveAll(current)
		_ = os.Rename(old, current)
		return fmt.Errorf("skills: record %s: %w", sk.Name, err)
	}
	if err := os.RemoveAll(old); err != nil {
		s.cfg.Logger.WarnContext(ctx, "the replaced copy of a skill could not be removed; the next start will", slog.String("skill", sk.Name), slog.Any("error", err))
	}
	return nil
}

// List implements ports.Skills.
func (s *Service) List(ctx context.Context) ([]domain.Skill, error) {
	rows, err := s.cfg.DB.DB().QueryContext(ctx, `SELECT `+columns+` FROM skills ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("skills: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Skill{}
	for rows.Next() {
		sk, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// Catalog implements ports.SkillCatalog: the enabled skills, by name, as each turn's prompt
// lists them (REQ-SKL-004). It reads the store at every call, so a change reaches the next
// turn (REQ-SKL-007).
func (s *Service) Catalog(ctx context.Context) ([]domain.CatalogEntry, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	return domain.Catalog(all), nil
}

// ReadSkill implements the tools module's SkillReader: an enabled skill's SKILL.md, or one
// file of its stored bundle (REQ-SKL-004). file is checked as a bundle entry and opened
// through an os.Root on the skill's directory, so no component — a `..`, a link swapped into
// the stored copy — can lead out of it.
func (s *Service) ReadSkill(ctx context.Context, name, file string) ([]byte, error) {
	sk, err := s.get(ctx, s.cfg.DB.DB(), name)
	if err != nil {
		return nil, err
	}
	if !sk.Enabled {
		return nil, fmt.Errorf("%w: skill %s is disabled", domain.ErrSkillNotFound, name)
	}
	if file == "" {
		file = domain.SkillFileName
	}
	entry, err := domain.CleanEntry(file)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.path(sk.Name))
	if err != nil {
		return nil, fmt.Errorf("skills: open %s: %w", name, err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(filepath.FromSlash(entry), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// `2026-09-skills-cli` decision 5: a file that is not there is VALIDATION_ERROR.
			return nil, fmt.Errorf("%w: skill %s has no file %s", domain.ErrSkillValidation, name, entry)
		}
		return nil, fmt.Errorf("%w: skill %s: %s cannot be read inside the bundle: %w", domain.ErrSkillValidation, name, entry, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("skills: stat %s/%s: %w", name, entry, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s in skill %s is not a file", domain.ErrSkillValidation, entry, name)
	}
	// Install bounded every file at MaxSkillFileBytes; the bound holds if the copy was changed.
	data, err := io.ReadAll(io.LimitReader(f, domain.MaxSkillFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("skills: read %s/%s: %w", name, entry, err)
	}
	if len(data) > domain.MaxSkillFileBytes {
		return nil, fmt.Errorf("%w: %s in skill %s is larger than 1 MiB", domain.ErrSkillValidation, entry, name)
	}
	return data, nil
}

// Get implements ports.Skills.
func (s *Service) Get(ctx context.Context, name string, withBody bool) (domain.Skill, string, error) {
	sk, err := s.get(ctx, s.cfg.DB.DB(), name)
	if err != nil || !withBody {
		return sk, "", err
	}
	md, err := os.ReadFile(filepath.Join(s.path(sk.Name), domain.SkillFileName)) // #nosec G304 -- a validated name under the store
	if err != nil {
		return domain.Skill{}, "", fmt.Errorf("skills: read %s: %w", sk.Name, err)
	}
	_, body, err := domain.ParseFrontMatter(string(md))
	if err != nil {
		return domain.Skill{}, "", fmt.Errorf("skills: stored %s no longer parses: %w", sk.Name, err)
	}
	return sk, body, nil
}

// SetEnabled implements ports.Skills.
func (s *Service) SetEnabled(ctx context.Context, name string, enabled bool) (domain.Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, err := s.get(ctx, s.cfg.DB.DB(), name)
	if err != nil {
		return domain.Skill{}, err
	}
	if sk.Enabled == enabled {
		return sk, nil
	}
	if enabled {
		var n int
		if err := s.cfg.DB.DB().QueryRowContext(ctx, `SELECT count(*) FROM skills WHERE enabled = 1`).Scan(&n); err != nil {
			return domain.Skill{}, fmt.Errorf("skills: count the enabled skills: %w", err)
		}
		if n >= domain.MaxEnabledSkills {
			return domain.Skill{}, fmt.Errorf("%w: %d skills are enabled, the most allowed; disable one first", domain.ErrSkillValidation, domain.MaxEnabledSkills)
		}
	}
	if _, err := s.cfg.DB.DB().ExecContext(ctx, `UPDATE skills SET enabled = ? WHERE id = ?`, enabled, sk.ID); err != nil {
		return domain.Skill{}, fmt.Errorf("skills: update %s: %w", name, err)
	}
	sk.Enabled = enabled
	change := domain.SkillDisabled
	if enabled {
		change = domain.SkillEnabled
	}
	s.publish(sk.Name, change)
	return sk, nil
}

// Remove implements ports.Skills: the row first, so a crash leaves only a directory Recover
// removes.
func (s *Service) Remove(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, err := s.get(ctx, s.cfg.DB.DB(), name)
	if err != nil {
		return err
	}
	if _, err := s.cfg.DB.DB().ExecContext(ctx, `DELETE FROM skills WHERE id = ?`, sk.ID); err != nil {
		return fmt.Errorf("skills: remove %s: %w", name, err)
	}
	if err := os.RemoveAll(s.path(sk.Name)); err != nil {
		s.cfg.Logger.WarnContext(ctx, "a removed skill's copy could not be deleted; the next start will", slog.String("skill", sk.Name), slog.Any("error", err))
	}
	s.publish(sk.Name, domain.SkillRemoved)
	return nil
}

func (s *Service) publish(name, change string) {
	if s.cfg.Bus != nil {
		s.cfg.Bus.Publish(ports.SkillChanged{Name: name, Change: change})
	}
}

func (s *Service) path(name string) string { return filepath.Join(s.cfg.Dir, name) }

// names maps every recorded skill to the digest of its stored copy.
func (s *Service) names(ctx context.Context) (map[string]string, error) {
	rows, err := s.cfg.DB.DB().QueryContext(ctx, `SELECT name, sha256 FROM skills`)
	if err != nil {
		return nil, fmt.Errorf("skills: read the names: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var n, sum string
		if err := rows.Scan(&n, &sum); err != nil {
			return nil, fmt.Errorf("skills: read the names: %w", err)
		}
		out[n] = sum
	}
	return out, rows.Err()
}

const columns = `id, name, description, version, source, sha256, size_bytes, enabled, installed_at`

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) get(ctx context.Context, q querier, name string) (domain.Skill, error) {
	sk, err := scan(q.QueryRowContext(ctx, `SELECT `+columns+` FROM skills WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Skill{}, fmt.Errorf("%w: %q", domain.ErrSkillNotFound, name)
	}
	return sk, err
}

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (domain.Skill, error) {
	var sk domain.Skill
	var version sql.NullString
	if err := r.Scan(&sk.ID, &sk.Name, &sk.Description, &version, &sk.Source, &sk.SHA256, &sk.SizeBytes, &sk.Enabled, &sk.InstalledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Skill{}, err
		}
		return domain.Skill{}, fmt.Errorf("skills: read a skill: %w", err)
	}
	sk.Version = version.String
	return sk, nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// writeBundle copies a checked bundle into dir with normalised modes: 0755 directories and
// 0644 files, so no exec, setuid, setgid or sticky bit survives (decision 1).
func writeBundle(dir string, b domain.Bundle) error {
	if err := os.Mkdir(dir, 0o755); err != nil { // #nosec G301 -- decision 1 normalises directories to 0755
		return fmt.Errorf("skills: create %s: %w", dir, err)
	}
	dirs := map[string]bool{dir: true}
	for p, data := range b.Contents {
		full := filepath.Join(dir, filepath.FromSlash(p))
		for d := filepath.Dir(full); d != dir; d = filepath.Dir(d) {
			dirs[d] = true
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { // #nosec G301 -- as above
			return fmt.Errorf("skills: create %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil { // #nosec G306 G302 -- decision 1: files are 0644 text
			return fmt.Errorf("skills: write %s: %w", p, err)
		}
		// The umask may have taken bits off; the mode is set, not left to it.
		if err := os.Chmod(full, 0o644); err != nil { // #nosec G302 -- decision 1: files are 0644 text
			return fmt.Errorf("skills: chmod %s: %w", p, err)
		}
	}
	for d := range dirs {
		if err := os.Chmod(d, 0o755); err != nil { // #nosec G302 -- decision 1 normalises directories to 0755
			return fmt.Errorf("skills: chmod %s: %w", d, err)
		}
	}
	return nil
}
