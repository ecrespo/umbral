package pluginstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	ctxdomain "github.com/ecrespo/umbral/internal/context/domain"
	mcpdomain "github.com/ecrespo/umbral/internal/mcp/domain"
	"github.com/ecrespo/umbral/internal/plugins/domain"
	"github.com/ecrespo/umbral/internal/store"
)

const manifest = `name = "acme"
version = "1.0.0"
description = "Acme tools"

[[commands]]
name = "deploy"
argv = ["make", "deploy"]

[[mcp_servers]]
name = "acme"
transport = "stdio"
command = "acme-mcp"
args = ["--stdio"]

[[skills]]
path = "skills/review"

[[detection]]
path = "detect/acme.toml"

[[keys]]
key = "ctrl+alt+d"
command = "deploy"
`

// writePlugin lays a plugin bundle out in a fresh directory.
func writePlugin(t *testing.T, manifestText string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		domain.ManifestFile:      manifestText,
		"skills/review/SKILL.md": "---\nname: acme-review\ndescription: Review things\n---\nBody\n",
		"detect/acme.toml":       "name = \"acme\"\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// dirBundles reads a directory's regular files: the skill store's reader without its limits,
// which its own tests cover.
type dirBundles struct{}

func (dirBundles) ReadFiles(root, manifestFile string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = data
		return err
	})
	if _, ok := files[manifestFile]; err == nil && !ok {
		err = fmt.Errorf("%w: no %s", ctxdomain.ErrSkillValidation, manifestFile)
	}
	return files, err
}

type okDetection struct{}

func (okDetection) Check([]byte) error { return nil }

type fakeServers struct {
	mu      sync.Mutex
	servers map[string]mcpdomain.AddParams
	failAdd bool
	// checkErr is what Check answers, as the MCP module does for a url the redactor would
	// change or a secret reference the backend refuses.
	checkErr error
	// beforeAdd runs ahead of each Add, under the lock: another client adding a server at
	// that moment.
	beforeAdd func(p mcpdomain.AddParams)
}

func (f *fakeServers) Check(context.Context, mcpdomain.AddParams) error { return f.checkErr }

func (f *fakeServers) List(context.Context) ([]mcpdomain.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mcpdomain.Server, 0, len(f.servers))
	for name, p := range f.servers {
		out = append(out, mcpdomain.Server{Name: name, Trust: p.Trust})
	}
	return out, nil
}

func (f *fakeServers) Add(_ context.Context, p mcpdomain.AddParams) (mcpdomain.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAdd {
		return mcpdomain.Server{}, errors.New("cannot add")
	}
	if f.beforeAdd != nil {
		f.beforeAdd(p)
	}
	if _, ok := f.servers[p.Name]; ok {
		return mcpdomain.Server{}, mcpdomain.ErrConflict
	}
	f.servers[p.Name] = p
	return mcpdomain.Server{Name: p.Name, Trust: p.Trust}, nil
}

func (f *fakeServers) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.servers[name]; !ok {
		return mcpdomain.ErrNotFound
	}
	delete(f.servers, name)
	return nil
}

type fakeSkills struct {
	mu          sync.Mutex
	skills      map[string]string // name → the path it was installed from
	digests     map[string]string // name → its digest
	failInstall bool
}

func (f *fakeSkills) List(context.Context) ([]ctxdomain.Skill, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ctxdomain.Skill, 0, len(f.skills))
	for name := range f.skills {
		out = append(out, ctxdomain.Skill{Name: name, SHA256: f.digests[name]})
	}
	return out, nil
}

func (f *fakeSkills) Install(_ context.Context, p ctxdomain.InstallParams) (ctxdomain.Skill, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failInstall {
		return ctxdomain.Skill{}, errors.New("cannot install")
	}
	md, err := os.ReadFile(filepath.Join(p.Path, ctxdomain.SkillFileName))
	if err != nil {
		return ctxdomain.Skill{}, err
	}
	fm, _, err := ctxdomain.ParseFrontMatter(string(md))
	if err != nil {
		return ctxdomain.Skill{}, err
	}
	if want := ctxdomain.Digest(map[string][]byte{ctxdomain.SkillFileName: md}); p.ExpectedSHA256 != want {
		return ctxdomain.Skill{}, fmt.Errorf("digest %s, want %s", p.ExpectedSHA256, want)
	}
	f.skills[fm.Name] = p.Path
	f.digests[fm.Name] = p.ExpectedSHA256
	return ctxdomain.Skill{Name: fm.Name, SHA256: p.ExpectedSHA256}, nil
}

func (f *fakeSkills) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.skills, name)
	delete(f.digests, name)
	return nil
}

type rig struct {
	svc     *Service
	dir     string
	servers *fakeServers
	skills  *fakeSkills
	db      *store.Store
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(t.Context(), store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := &rig{
		dir: filepath.Join(filepath.Dir(db.Path()), "plugins"), db: db,
		servers: &fakeServers{servers: map[string]mcpdomain.AddParams{}},
		skills:  &fakeSkills{skills: map[string]string{}, digests: map[string]string{}},
	}
	r.svc, err = New(Config{
		DB: db, Dir: r.dir, Bundles: dirBundles{}, Detection: okDetection{},
		Servers: r.servers, Skills: r.skills, NewID: func() string { return store.NewID(store.PrefixPlugin) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// stored lists what the plugins directory holds, the owner marker aside.
func (r *rig) stored(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != ownerMarker {
			names = append(names, e.Name())
		}
	}
	return names
}

// TestInspectShowsTheManifestAndWritesNothing_REQ_PLG_001: inspect reads the manifest and every
// contribution, gives the digest, and writes and adds nothing.
func TestInspectShowsTheManifestAndWritesNothing_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	m := insp.Manifest
	if m.Name != "acme" || len(m.Commands) != 1 || m.Commands[0].Argv[1] != "deploy" ||
		len(m.Servers) != 1 || m.Servers[0].Command != "acme-mcp" || m.Skills[0].Name != "acme-review" ||
		len(m.Keys) != 1 || len(m.Detection) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	if len(insp.SHA256) != 64 || len(insp.Files) != 3 || insp.SizeBytes == 0 {
		t.Fatalf("inspection = %+v", insp)
	}
	if got := r.stored(t); len(got) != 0 || len(r.servers.servers) != 0 || len(r.skills.skills) != 0 {
		t.Fatalf("inspect wrote %v, added servers %v and skills %v", got, r.servers.servers, r.skills.skills)
	}
}

// TestAnUnknownKeyRefusesTheManifest_REQ_PLG_002: a `trust` key, or any other the manifest does
// not define, refuses the bundle, so a plugin cannot declare its server trusted.
func TestAnUnknownKeyRefusesTheManifest_REQ_PLG_002(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, strings.Replace(manifest, `args = ["--stdio"]`, "args = [\"--stdio\"]\ntrust = \"trusted\"", 1))
	_, err := r.svc.Inspect(t.Context(), src)
	if !errors.Is(err, domain.ErrValidation) || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("Inspect = %v, want a validation error naming the trust key", err)
	}
}

// TestInstallAddsTheServerUntrustedAndTheSkill_REQ_PLG_002 is F2f's exit criterion at the
// store: the inspected digest installs a copy, an untrusted MCP server and the skill, read from
// the stored copy.
func TestInstallAddsTheServerUntrustedAndTheSkill_REQ_PLG_002(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.svc.Install(t.Context(), src, insp.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if p.SHA256 != insp.SHA256 || p.Source != src || !strings.HasPrefix(p.ID, "plg_") {
		t.Errorf("plugin = %+v", p)
	}
	if got := r.servers.servers["acme"]; got.Trust != mcpdomain.TrustUntrusted || got.Command != "acme-mcp" {
		t.Errorf("the server was added as %+v, want untrusted", got)
	}
	if from := r.skills.skills["acme-review"]; from != filepath.Join(r.dir, "acme", "skills", "review") {
		t.Errorf("the skill was installed from %q, want the stored copy", from)
	}
	if got := r.stored(t); !slices.Equal(got, []string{"acme"}) {
		t.Errorf("the plugins directory holds %v, want acme", got)
	}
	listed, err := r.svc.List(t.Context())
	if err != nil || len(listed) != 1 || listed[0].Manifest.Commands[0].Name != "deploy" {
		t.Fatalf("List = %+v, %v", listed, err)
	}
}

// TestInstallTakesOnlyTheInspectedDigest_REQ_PLG_001: a bundle changed after inspect installs
// nothing.
func TestInstallTakesOnlyTheInspectedDigest_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "detect", "acme.toml"), []byte("name = \"other\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Install of a changed bundle = %v, want ErrConflict", err)
	}
	if got := r.stored(t); len(got) != 0 || len(r.servers.servers) != 0 {
		t.Fatalf("a refused install left %v and servers %v", got, r.servers.servers)
	}
}

// TestATakenNameRefusesBeforeWriting_REQ_PLG_001: a server name the user already has is a
// conflict found before anything is written, so the user's server is untouched.
func TestATakenNameRefusesBeforeWriting_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	r.servers.servers["acme"] = mcpdomain.AddParams{Name: "acme", Trust: mcpdomain.TrustTrusted}
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Install = %v, want ErrConflict", err)
	}
	if got := r.stored(t); len(got) != 0 || len(r.skills.skills) != 0 || r.servers.servers["acme"].Trust != mcpdomain.TrustTrusted {
		t.Fatalf("a refused install left %v, skills %v, servers %v", got, r.skills.skills, r.servers.servers)
	}
}

// TestAFailedContributionLeavesNothing_REQ_PLG_001: the install fails whole when a skill cannot
// be installed — the server it had added goes, and so do the row and the copy.
func TestAFailedContributionLeavesNothing_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	r.skills.failInstall = true
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); err == nil {
		t.Fatal("an install whose skill failed succeeded")
	}
	if got := r.stored(t); len(got) != 0 || len(r.servers.servers) != 0 {
		t.Fatalf("a failed install left %v and servers %v", got, r.servers.servers)
	}
	if listed, _ := r.svc.List(t.Context()); len(listed) != 0 {
		t.Fatalf("a failed install left a row: %+v", listed)
	}
}

// TestRemoveTakesItsContributionsAway_REQ_PLG_001 removes the plugin's server, skill, row and
// copy.
func TestRemoveTakesItsContributionsAway_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Remove(t.Context(), "acme"); err != nil {
		t.Fatal(err)
	}
	if got := r.stored(t); len(got) != 0 || len(r.servers.servers) != 0 || len(r.skills.skills) != 0 {
		t.Fatalf("remove left %v, servers %v, skills %v", got, r.servers.servers, r.skills.skills)
	}
	if err := r.svc.Remove(t.Context(), "acme"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a second remove = %v, want ErrNotFound", err)
	}
}

// TestAKeyAnotherPluginBindsIsAConflict_REQ_PLG_001: a plugin cannot take a key another one
// bound (decision 8).
func TestAKeyAnotherPluginBindsIsAConflict_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, _ := r.svc.Inspect(t.Context(), src)
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); err != nil {
		t.Fatal(err)
	}
	other := strings.NewReplacer(`name = "acme"`+"\nversion", `name = "beta"`+"\nversion",
		"[[mcp_servers]]\nname = \"acme\"", "[[mcp_servers]]\nname = \"beta\"",
		"[[skills]]\npath = \"skills/review\"\n", "").Replace(manifest)
	src2 := writePlugin(t, other)
	insp2, err := r.svc.Inspect(t.Context(), src2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Install(t.Context(), src2, insp2.SHA256); !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "ctrl+alt+d") {
		t.Fatalf("Install of a second plugin binding ctrl+alt+d = %v, want a conflict naming the key", err)
	}
}

// TestInspectRefusesWhatTheServerModuleWould_REQ_PLG_001: a server mcp.server.add would refuse
// — a url the redactor changes, a secret reference the backend refuses — is refused at inspect,
// before the user confirms anything (decision 2).
func TestInspectRefusesWhatTheServerModuleWould_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	r.servers.checkErr = fmt.Errorf("%w: env: references are not accepted", mcpdomain.ErrConfigInvalid)
	if _, err := r.svc.Inspect(t.Context(), writePlugin(t, manifest)); !errors.Is(err, domain.ErrValidation) || !strings.Contains(err.Error(), "acme") {
		t.Fatalf("Inspect = %v, want a validation error naming the server", err)
	}
}

// TestARollbackUndoesOnlyWhatThisInstallAdded_REQ_PLG_001: a server another client adds under
// the same name between the check and the add fails the install, and the rollback leaves that
// client's server alone (decision 4).
func TestARollbackUndoesOnlyWhatThisInstallAdded_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	// Untrusted, as `umb mcp add` always adds: trust cannot tell it from the plugin's.
	r.servers.beforeAdd = func(p mcpdomain.AddParams) {
		r.servers.servers[p.Name] = mcpdomain.AddParams{Name: p.Name, Command: "theirs", Trust: mcpdomain.TrustUntrusted}
	}
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); err == nil {
		t.Fatal("an install whose server name was taken meanwhile succeeded")
	}
	if got := r.servers.servers["acme"]; got.Command != "theirs" {
		t.Fatalf("the rollback removed or changed the other client's server: %+v", r.servers.servers)
	}
	if got := r.stored(t); len(got) != 0 {
		t.Fatalf("a failed install left %v", got)
	}
}

// TestRemoveLeavesWhatTheUserReplaced_REQ_PLG_002: a server the user made trusted, by removing
// it and adding it again, and a skill the user replaced under the plugin's name are the user's,
// and removing the plugin keeps them (decisions 5 and 9).
func TestRemoveLeavesWhatTheUserReplaced_REQ_PLG_002(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); err != nil {
		t.Fatal(err)
	}
	r.servers.servers["acme"] = mcpdomain.AddParams{Name: "acme", Trust: mcpdomain.TrustTrusted}
	r.skills.digests["acme-review"] = strings.Repeat("b", 64)
	if err := r.svc.Remove(t.Context(), "acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.servers.servers["acme"]; !ok {
		t.Error("removing the plugin removed the server the user had made trusted")
	}
	if _, ok := r.skills.skills["acme-review"]; !ok {
		t.Error("removing the plugin removed the skill the user had replaced")
	}
	if got := r.stored(t); len(got) != 0 {
		t.Errorf("remove left %v", got)
	}
}

// TestRecoverUndoesAnInstallACrashInterrupted_REQ_PLG_001: a row still `installing` at start
// is an install a crash stopped between the row and its last contribution; Data Model §6
// step 10 undoes it whole, so an install never stays half done.
func TestRecoverUndoesAnInstallACrashInterrupted_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	src := writePlugin(t, manifest)
	insp, err := r.svc.Inspect(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	r.skills.failInstall = true
	r.svc.crashAfterServers = true
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); err == nil {
		t.Fatal("the simulated crash did not stop the install")
	}
	if _, ok := r.servers.servers["acme"]; !ok || len(r.stored(t)) != 1 {
		t.Fatalf("the crash should leave the server and the copy: servers %v, stored %v", r.servers.servers, r.stored(t))
	}
	if listed, _ := r.svc.List(t.Context()); len(listed) != 0 {
		t.Fatalf("an unfinished install is listed: %+v", listed)
	}
	if err := r.svc.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.stored(t); len(got) != 0 || len(r.servers.servers) != 0 {
		t.Fatalf("Recover left %v and servers %v", got, r.servers.servers)
	}
	var rows int
	if err := r.db.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM plugins`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("Recover left %d rows (%v)", rows, err)
	}
}

// TestRecoverRemovesWhatNoRowRecords is Data Model §6 step 10.
func TestRecoverRemovesWhatNoRowRecords(t *testing.T) {
	r := newRig(t)
	for _, d := range []string{".staging-plg_x", "orphan"} {
		if err := os.MkdirAll(filepath.Join(r.dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	src := writePlugin(t, manifest)
	insp, _ := r.svc.Inspect(t.Context(), src)
	if _, err := r.svc.Install(t.Context(), src, insp.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.stored(t); !slices.Equal(got, []string{"acme"}) {
		t.Fatalf("after Recover the directory holds %v, want acme", got)
	}
}

// TestMigration0010Constraints_REQ_PLG_001 checks the table's CHECKs.
func TestMigration0010Constraints_REQ_PLG_001(t *testing.T) {
	r := newRig(t)
	insert := func(id, name, sha, manifestJSON string) error {
		_, err := r.db.DB().ExecContext(t.Context(),
			`INSERT INTO plugins(id, name, version, description, source, sha256, size_bytes, manifest_json, installed_at)
			 VALUES (?, ?, '1', 'd', '/x', ?, 1, ?, 0)`, id, name, sha, manifestJSON)
		return err
	}
	sha := strings.Repeat("a", 64)
	if err := insert("plg_1", "acme", sha, "{}"); err != nil {
		t.Fatalf("a valid row was refused: %v", err)
	}
	for _, bad := range [][4]string{
		{"skl_2", "b", sha, "{}"},
		{"plg_3", "Bad-Name", sha, "{}"},
		{"plg_4", "c", "short", "{}"},
		{"plg_5", "d", sha, "not json"},
		{"plg_6", "acme", sha, "{}"},
	} {
		if err := insert(bad[0], bad[1], bad[2], bad[3]); err == nil {
			t.Errorf("row %v was accepted", bad)
		}
	}
}
