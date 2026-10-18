package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalize_REQ_NTF_001(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
		limit    int
	}{
		{"build done", "build done", MaxTitleLen},
		{"  build \t\n  done  ", "build done", MaxTitleLen},
		{"\x1b]0;pwned\x07title", "]0;pwnedtitle", MaxTitleLen},
		{"a\u200bb", "ab", MaxTitleLen},
		{strings.Repeat("é", 81), strings.Repeat("é", 80), MaxTitleLen},
		{strings.Repeat("b", 241), strings.Repeat("b", 240), MaxBodyLen},
		{strings.Repeat("a", 79) + " b", strings.Repeat("a", 79), MaxTitleLen},
		{"\x00\x01", "", MaxTitleLen},
	}
	for _, c := range cases {
		if got := Normalize(c.in, c.limit); got != c.want {
			t.Errorf("Normalize(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

func TestRequestBuild_REQ_NTF_001(t *testing.T) {
	t.Parallel()
	n, err := Request{Title: " tests\tpassed ", Body: strings.Repeat("x", 300), Source: "ci-hook"}.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n.Title != "tests passed" || len([]rune(n.Body)) != MaxBodyLen || n.Sound != SoundNone || n.Source != "ci-hook" {
		t.Errorf("built %+v", n)
	}

	for name, r := range map[string]Request{
		"empty title":          {Title: " \x01 ", Source: "ci-hook"},
		"no source":            {Title: "t"},
		"bad source":           {Title: "t", Source: "a b"},
		"long source":          {Title: "t", Source: strings.Repeat("s", MaxSourceLen+1)},
		"reserved source":      {Title: "t", Source: "umbral:agent"},
		"unknown sound":        {Title: "t", Source: "ci-hook", Sound: "bell"},
		"control-only source":  {Title: "t", Source: "\x07"},
		"source with a slash":  {Title: "t", Source: "a/b"},
		"source with an equal": {Title: "t", Source: "a=b"},
	} {
		if _, err := r.Build(); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: Build = %v, want ErrValidation", name, err)
		}
	}

	for _, s := range []Sound{SoundNone, SoundDone, SoundRequest} {
		if _, err := (Request{Title: "t", Source: "x", Sound: s}).Build(); err != nil {
			t.Errorf("sound %q: %v", s, err)
		}
	}
}
