package domain

import (
	"encoding/json"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type corpusCase struct {
	Name  string   `json:"name"`
	Input string   `json:"input"`
	Rules []string `json:"rules"`
}

type corpus struct {
	Positive []corpusCase `json:"positive"`
	Negative []corpusCase `json:"negative"`
}

var classes = map[string]string{
	"alnum":     "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
	"upper":     "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
	"hex":       "0123456789abcdef",
	"digits":    "0123456789",
	"b64":       "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/",
	"b64url":    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
	"crockford": "0123456789ABCDEFGHJKMNPQRSTVWXYZ",
}

var template = regexp.MustCompile(`\{\{rand:(\d+):(\w+)\}\}`)

// expand replaces every {{rand:N:CLASS}} with N characters of CLASS from a PRNG seeded with
// the case name, and returns the fragments it generated, so the test can check that none of
// them survives redaction. {{PRIVATE}} becomes the literal word.
func expand(t *testing.T, name, input string) (string, []string) {
	t.Helper()
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	rng := rand.New(rand.NewPCG(h.Sum64(), 0x5ec))
	var fragments []string
	out := template.ReplaceAllStringFunc(input, func(m string) string {
		parts := template.FindStringSubmatch(m)
		n, _ := strconv.Atoi(parts[1])
		alphabet, ok := classes[parts[2]]
		if !ok {
			t.Fatalf("%s: unknown class %q", name, parts[2])
		}
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		fragments = append(fragments, string(b))
		return string(b)
	})
	return strings.ReplaceAll(out, "{{PRIVATE}}", "PRIVATE"), fragments
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "redact", "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRedactionCorpus_REQ_SEC_001: every secret of the positive corpus is replaced by
// `[REDACTED:<rule>]` naming the rule that caught it, no generated fragment survives, and
// every line of the negative corpus comes back untouched. The corpus holds at least 30 of
// each (the task's Done line).
func TestRedactionCorpus_REQ_SEC_001(t *testing.T) {
	t.Parallel()

	c := loadCorpus(t)
	if len(c.Positive) < 30 || len(c.Negative) < 30 {
		t.Fatalf("corpus has %d positives and %d negatives, want at least 30 of each", len(c.Positive), len(c.Negative))
	}

	for _, tc := range c.Positive {
		t.Run("positive/"+tc.Name, func(t *testing.T) {
			t.Parallel()
			input, fragments := expand(t, tc.Name, tc.Input)
			got := Redact(input)

			want := map[string]int{}
			for _, r := range tc.Rules {
				want[r]++
			}
			for rule, n := range want {
				if c := strings.Count(got.Text, "[REDACTED:"+rule+"]"); c != n || got.Counts[rule] != n {
					t.Errorf("rule %s: %d placeholders, count %d, want %d\n%s", rule, c, got.Counts[rule], n, got.Text)
				}
			}
			for rule, n := range got.Counts {
				if want[rule] != n {
					t.Errorf("rule %s fired %d times, want %d\n%s", rule, n, want[rule], got.Text)
				}
			}
			for _, f := range fragments {
				if len(f) >= 8 && strings.Contains(got.Text, f) {
					t.Errorf("a secret fragment survived: %q in\n%s", f, got.Text)
				}
				// A rule that stops short leaves the key's tail right after its placeholder.
				if len(f) >= 20 && strings.Contains(got.Text, "]"+f[len(f)-4:]) {
					t.Errorf("the tail of a secret survived: %q in\n%s", f[len(f)-4:], got.Text)
				}
			}
		})
	}

	for _, tc := range c.Negative {
		t.Run("negative/"+tc.Name, func(t *testing.T) {
			t.Parallel()
			input, _ := expand(t, tc.Name, tc.Input)
			if got := Redact(input); got.Text != input || len(got.Counts) != 0 {
				t.Errorf("redacted what is not a secret: %v\n%s", got.Counts, got.Text)
			}
		})
	}
}

// TestTheEntropyDetectorsThresholds: the generic detector fires only at ≥ 4.5 bits per
// character and ≥ 20 characters (REQ-SEC-001). Entropy is measured over the string's own
// characters, so no string shorter than 23 can reach 4.5 bits (log2 22 ≈ 4.46).
func TestTheEntropyDetectorsThresholds(t *testing.T) {
	t.Parallel()

	const distinct = "q7Xk2Lm9Pz4Rw8Tn3Vb6Yc1Hd5Jf0GsQ"
	for _, tc := range []struct {
		s    string
		want bool
	}{
		{distinct[:22], false}, // 4.46 bits
		{distinct[:23], true},  // 4.52 bits
		{distinct[:19], false}, // under the length floor
		{strings.Repeat("ab", 20), false},
	} {
		got := Redact("value " + tc.s + " end")
		if fired := got.Counts[RuleHighEntropy] == 1; fired != tc.want {
			t.Errorf("%q (%.2f bits): fired = %v, want %v", tc.s, shannon(tc.s), fired, tc.want)
		}
	}
	if e := shannon(""); e != 0 {
		t.Errorf("shannon of nothing = %v", e)
	}
}

// TestRedactionIsIdempotent: redacting redacted text changes nothing, so a message that
// passes the egress edge twice is not mangled.
func TestRedactionIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, tc := range loadCorpus(t).Positive {
		input, _ := expand(t, tc.Name, tc.Input)
		once := Redact(input).Text
		if twice := Redact(once); twice.Text != once || len(twice.Counts) != 0 {
			t.Errorf("%s: a second pass changed %v\n%s", tc.Name, twice.Counts, twice.Text)
		}
	}
}

// TestARuleSetCanBeReplaced: the built-in rules are the default, and a bundle's rules replace
// them (REQ-SEC-010, T-F1-29) — with the entropy detector still on.
func TestARuleSetCanBeReplaced(t *testing.T) {
	t.Parallel()

	r := Redactor{
		Rules:   []RedactionRule{{Name: "acme", Expr: regexp.MustCompile(`acme_[a-z]{6}`)}},
		Entropy: true,
	}
	got := r.Redact("acme_abcdef and sk-" + strings.Repeat("a1", 12))
	if got.Counts["acme"] != 1 || got.Counts["openai_key"] != 0 {
		t.Errorf("counts = %v, want only acme", got.Counts)
	}
	if got := (Redactor{}).Redact("q7Xk2Lm9Pz4Rw8Tn3Vb6Yc1Hd5Jf0Gs"); len(got.Counts) != 0 {
		t.Errorf("a redactor with entropy off fired: %v", got.Counts)
	}
}

// TestRandomKeysAreCaught: the generic detector is for keys no named rule knows. At 40
// characters REQ-SEC-001's 4.5-bit floor passes about 98 % of random alphanumeric or base64
// strings, and the identifier filter must not eat into that. (At 32 characters the floor
// alone passes about 70 %, and at 24 about 5 %: short unknown keys rely on the named rules.)
func TestRandomKeysAreCaught(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(1, 2))
	for _, class := range []string{"alnum", "b64", "b64url"} {
		alphabet, caught := classes[class], 0
		const n = 2000
		for range n {
			b := make([]byte, 40)
			for j := range b {
				b[j] = alphabet[rng.IntN(len(alphabet))]
			}
			if Redact(string(b)).Counts[RuleHighEntropy] == 1 {
				caught++
			}
		}
		if caught*100 < n*95 {
			t.Errorf("%s: caught %d of %d random 40-character keys, want at least 95 %%", class, caught, n)
		}
	}
}

// TestWordLikeSplitsLikeAnIdentifier pins the identifier filter on both sides of its
// one-in-ten threshold.
func TestWordLikeSplitsLikeAnIdentifier(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		tok  string
		want bool
	}{
		{"TestPolicyAutoEditWorkspace_REQ_AGT_013", true},
		{"kubernetes-deployment-configuration-v2", true},            // 2 short of 34: 6 %
		{"HTTPServerConfigurationLoader", true},                     // an acronym joins its word
		{"alphaBetaGammaDeltaEpsilonZetaEtaThetaIotaKappaX1", true}, // 2 short of 45: 4 %
		{"alphaBetaGammaDeltaX1Y2", false},                          // 4 short of 23: 17 %
		{"alphaBetaGammaDeltaEpsilonZetaX1Y2", false},               // 4 short of 34: 12 %
		{"q7Xk2Lm9Pz4Rw8Tn3Vb6Yc1Hd5Jf0Gs", false},
		{"----", false},
	} {
		if got := wordLike(tc.tok); got != tc.want {
			t.Errorf("wordLike(%q) = %v, want %v", tc.tok, got, tc.want)
		}
	}
}

// TestUmbralIDsAreNeverRedacted: a type-prefixed ULID is random by construction, and some
// cross every other threshold — the model still needs them to address threads and blocks.
func TestUmbralIDsAreNeverRedacted(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(7, 7))
	alphabet, pastEverythingElse := classes["crockford"], 0
	for range 500 {
		b := make([]byte, 26)
		for j := range b {
			b[j] = alphabet[rng.IntN(len(alphabet))]
		}
		id := "thr_" + string(b)
		if shannon(id) >= entropyMinBits && !wordLike(id) {
			pastEverythingElse++
		}
		if got := Redact("thread " + id); len(got.Counts) != 0 {
			t.Fatalf("%s was redacted: %s", id, got.Text)
		}
	}
	if pastEverythingElse == 0 {
		t.Error("no id crossed the other thresholds; this test proves nothing about the id filter")
	}
}
