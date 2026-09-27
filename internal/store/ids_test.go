package store

import (
	"strings"
	"testing"
)

func TestNewIDIsPrefixedAndUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := NewID(PrefixBlock)
		if !strings.HasPrefix(id, "blk_") {
			t.Fatalf("NewID = %q, want a blk_ prefix", id)
		}
		if len(id) != len("blk_")+26 {
			t.Fatalf("NewID = %q, want a 26-character ULID after the prefix", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewID produced %q twice", id)
		}
		seen[id] = struct{}{}
	}
}

// TestNewIDSatisfiesTheSchemaCheck ties the generator to the CHECK constraints in
// migration 0001: an id the schema would reject is a bug here, not in the caller.
func TestNewIDSatisfiesTheSchemaCheck(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	_, err := s.DB().ExecContext(t.Context(),
		`INSERT INTO sessions(id, shell, cwd, cols, rows, state, created_at)
		 VALUES (?, '/bin/sh', '/', 80, 24, 'alive', 0)`, NewID(PrefixSession))
	if err != nil {
		t.Fatalf("a generated session id was rejected by the schema: %v", err)
	}
}

func TestParsePrefix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		id      string
		want    string
		wantErr bool
	}{
		{id: "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B", want: "blk"},
		{id: "tc_01J9Z3K8T2QH6W4V5X7Y8Z9A0B", want: "tc"},
		{id: "nounderscore", wantErr: true},
		{id: "_leading", wantErr: true},
		{id: "", wantErr: true},
	} {
		got, err := ParsePrefix(tc.id)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePrefix(%q) = %q, want an error", tc.id, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePrefix(%q): %v", tc.id, err)
		}
		if got != tc.want {
			t.Errorf("ParsePrefix(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
