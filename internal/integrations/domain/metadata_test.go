package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeDisplay_REQ_INT_004(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"go test", "go test"},
		{"  go \t\n  test  ", "go test"},
		{"\x1b]0;pwned\x07title", "]0;pwnedtitle"},
		{"a\u200bb", "ab"},
		{strings.Repeat("é", 81), strings.Repeat("é", 80)},
		{strings.Repeat("a", 79) + " b", strings.Repeat("a", 79)},
		{"\x00\x01", ""},
	}
	for _, c := range cases {
		if got := NormalizeDisplay(c.in); got != c.want {
			t.Errorf("NormalizeDisplay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMetadataReportBuild_REQ_INT_004(t *testing.T) {
	t.Parallel()
	s := func(v string) *string { return &v }
	ok := MetadataFields{PaneID: "w1:p1", Source: "claude-code"}

	r, err := MetadataFields{
		PaneID: "w1:p1", Source: "claude-code",
		Title: s(" go\ttest "), TitleSent: true, DisplayNameSent: true,
		StateLabels: map[string]*string{"blocked": s("needs you")},
		Tokens:      map[string]*string{"summary": s("unit"), "old": nil, "blank": s(" \x01 ")},
		TTLMillis:   ms(1000),
	}.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Set[KeyTitle] != "go test" || r.Set["label:blocked"] != "needs you" || r.Set["token:summary"] != "unit" || len(r.Set) != 3 {
		t.Errorf("Set = %v", r.Set)
	}
	slices.Sort(r.Clear)
	if !slices.Equal(r.Clear, []string{KeyDisplayName, "token:blank", "token:old"}) {
		t.Errorf("Clear = %v: null and a value that normalizes to nothing both clear", r.Clear)
	}

	// A null object clears every key of that kind the source holds, and is a report by itself.
	r, err = with2(ok, func(f *MetadataFields) { f.TokensNull, f.StateLabelsNull = true, true }).Build()
	if err != nil {
		t.Fatalf("Build with null objects: %v", err)
	}
	slices.Sort(r.ClearPrefixes)
	if !slices.Equal(r.ClearPrefixes, []string{KeyLabelPrefix, KeyTokenPrefix}) || len(r.Set)+len(r.Clear) != 0 {
		t.Errorf("null objects built %+v", r)
	}

	tokens := map[string]*string{}
	for i := range MaxTokensPerReport + 1 {
		tokens["t"+strings.Repeat("x", i)] = s("v")
	}
	bad := []struct {
		name string
		f    MetadataFields
	}{
		{"nothing to report", ok},
		{"17 tokens", with2(ok, func(f *MetadataFields) { f.Tokens = tokens })},
		{"a label for an invented state", with2(ok, func(f *MetadataFields) { f.StateLabels = map[string]*string{"thinking": s("x")} })},
		{"a token name with a space", with2(ok, func(f *MetadataFields) { f.Tokens = map[string]*string{"a b": s("x")} })},
		{"a token name of 27", with2(ok, func(f *MetadataFields) { f.Tokens = map[string]*string{strings.Repeat("a", 27): s("x")} })},
		{"a ttl past a day", with2(ok, func(f *MetadataFields) { f.TitleSent, f.Title, f.TTLMillis = true, s("x"), ms(MaxTTLMillis+1) })},
		{"a ttl of 0", with2(ok, func(f *MetadataFields) { f.TitleSent, f.Title, f.TTLMillis = true, s("x"), ms(0) })},
		{"a reserved source", with2(ok, func(f *MetadataFields) { f.Source, f.TitleSent, f.Title = "umbral:shell", true, s("x") })},
		{"a negative seq", with2(ok, func(f *MetadataFields) { n := int64(-1); f.TitleSent, f.Title, f.Seq = true, s("x"), &n })},
	}
	for _, c := range bad {
		if _, err := c.f.Build(); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: %v, want a validation error", c.name, err)
		}
	}
	if _, err := with2(ok, func(f *MetadataFields) { f.Tokens = map[string]*string{strings.Repeat("a", 26): s("x")} }).Build(); err != nil {
		t.Errorf("a token name of 26: %v", err)
	}
}

func ms(n int64) *int64 { return &n }

func with2(f MetadataFields, change func(*MetadataFields)) MetadataFields {
	change(&f)
	return f
}
