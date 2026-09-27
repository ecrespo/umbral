package domain

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRulesCandidatesArePrecedenceOrdered_REQ_CTX_001(t *testing.T) {
	root := filepath.FromSlash("/repo")
	cwd := filepath.FromSlash("/repo/svc/api")
	got := RulesCandidates(root, cwd)

	// The closest directory first; within it AGENTS → CLAUDE → WARP → CRUSH, each .local.md
	// above its own file.
	want := make([]string, 0, 24)
	for _, dir := range []string{"/repo/svc/api", "/repo/svc", "/repo"} {
		for _, name := range []string{
			"AGENTS.local.md", "AGENTS.md", "CLAUDE.local.md", "CLAUDE.md",
			"WARP.local.md", "WARP.md", "CRUSH.local.md", "CRUSH.md",
		} {
			want = append(want, filepath.Join(filepath.FromSlash(dir), name))
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("candidates:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRulesCandidatesOutsideTheRootAreTheCwdOnly_REQ_CTX_001(t *testing.T) {
	got := RulesCandidates(filepath.FromSlash("/other"), filepath.FromSlash("/repo/a"))
	if len(got) != 8 || filepath.Dir(got[0]) != filepath.FromSlash("/repo/a") {
		t.Fatalf("a root that is not above the cwd must search the cwd alone, got %v", got)
	}
}

func TestAttachmentTruncatedInDomain_REQ_CTX_005(t *testing.T) {
	content := []byte(strings.Repeat("a", 300<<10))
	a := NewAttachment(KindFile, "big.txt", content, int64(len(content)))
	if len(a.Content) != MaxAttachmentBytes {
		t.Fatalf("kept %d bytes, want %d", len(a.Content), MaxAttachmentBytes)
	}
	if a.Bytes != 300<<10 || a.TruncatedBytes != 44<<10 {
		t.Fatalf("bytes %d truncated %d", a.Bytes, a.TruncatedBytes)
	}
	if r := a.Render(); !strings.Contains(r, "45056 bytes omitted") {
		t.Fatalf("the context must say how many bytes were omitted:\n%s", r[len(r)-200:])
	}

	small := NewAttachment(KindFile, "s.txt", []byte("hi"), 2)
	if small.TruncatedBytes != 0 || strings.Contains(small.Render(), "omitted") {
		t.Fatalf("a small attachment is not truncated: %+v", small)
	}
	exact := NewAttachment(KindFile, "e.txt", content[:MaxAttachmentBytes], MaxAttachmentBytes)
	if under := NewAttachment(KindFile, "u.txt", []byte("abc"), 0); under.Bytes != 3 || under.TruncatedBytes != 0 {
		t.Fatalf("a total below what was read is what was read: %+v", under)
	}
	if exact.TruncatedBytes != 0 {
		t.Fatalf("an attachment of exactly the limit is not truncated: %d", exact.TruncatedBytes)
	}
}

func TestTruncationCutsOnARuneBoundary_REQ_CTX_005(t *testing.T) {
	// "é" is two bytes; put one across the limit.
	content := []byte(strings.Repeat("a", MaxAttachmentBytes-1) + "é" + "tail")
	a := NewAttachment(KindFile, "u.txt", content, int64(len(content)))
	if !utf8.ValidString(a.Content) {
		t.Fatal("a cut in the middle of a rune leaves invalid UTF-8")
	}
	if int64(len(a.Content))+a.TruncatedBytes != a.Bytes {
		t.Fatalf("kept %d + omitted %d != %d", len(a.Content), a.TruncatedBytes, a.Bytes)
	}
}

func TestTheTotalCountsBytesNeverRead_REQ_CTX_005(t *testing.T) {
	// The adapter reads at most the limit; the file's size says how much there was.
	a := NewAttachment(KindFile, "huge.log", []byte(strings.Repeat("x", MaxAttachmentBytes)), 10<<20)
	if a.TruncatedBytes != 10<<20-MaxAttachmentBytes {
		t.Fatalf("omitted %d", a.TruncatedBytes)
	}
}

func TestABinaryAttachmentIsNotInlined_REQ_CTX_002(t *testing.T) {
	a := NewAttachment(KindFile, "a.out", []byte("\x7fELF\x00\x00\x01"), 7)
	if a.Content != "" || !strings.Contains(a.Render(), "binary") {
		t.Fatalf("binary content must not reach the prompt: %+v", a)
	}
	if a.TruncatedBytes != 7 {
		t.Fatalf("the omitted bytes are the whole file, got %d", a.TruncatedBytes)
	}
}

func TestSystemPromptCarriesRulesAndGit_REQ_CTX_003(t *testing.T) {
	out, err := RenderSystem(SystemInput{
		Cwd:  "/repo/svc",
		Mode: "normal",
		Rules: []RulesFile{
			{Path: "/repo/svc/AGENTS.md", Content: "closest rule"},
			{Path: "/repo/AGENTS.md", Content: "root rule"},
		},
		Git: &GitContext{Branch: "main", Status: " M a.go", DiffStat: " a.go | 2 +-"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/repo/svc", "closest rule", "root rule", "Branch: main", " M a.go", "a.go | 2 +-"} {
		if !strings.Contains(out, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "closest rule") > strings.Index(out, "root rule") {
		t.Fatal("rules are listed highest precedence first")
	}

	noGit, err := RenderSystem(SystemInput{Cwd: "/tmp/x", Mode: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(noGit, "Branch:") {
		t.Fatalf("outside a repository there is no git section:\n%s", noGit)
	}
}

func TestUserMessageCarriesItsAttachments_REQ_CTX_002(t *testing.T) {
	out, err := RenderUser(UserInput{
		Text: "fix it",
		Attachments: []Attachment{
			NewAttachment(KindBlock, "blk_1", []byte("$ go test\nFAIL"), 14),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := RenderUser(UserInput{Text: "hi"}); plain != "hi" {
		t.Fatalf("a message without attachments is its text: %q", plain)
	}
	if !strings.HasPrefix(out, "fix it") || !strings.Contains(out, `kind="block" ref="blk_1"`) || !strings.Contains(out, "FAIL") {
		t.Fatalf("user message:\n%s", out)
	}
}

func TestAnUnreadableRepositoryIsStated_REQ_CTX_003(t *testing.T) {
	out, err := RenderSystem(SystemInput{Cwd: "/r", Mode: "normal", Git: &GitContext{Root: "/r", Unavailable: "git status: signal: killed"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "could not be read: git status: signal: killed") || strings.Contains(out, "Branch:") {
		t.Fatalf("system prompt:\n%s", out)
	}
}
