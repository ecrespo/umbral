package domain

import (
	"errors"
	"testing"
)

func TestARemoteIsReachedOverSSHOnly_REQ_RMT_001(t *testing.T) {
	good := map[string]Target{
		"ssh://box":                {Host: "box"},
		"ssh://me@box.example.org": {User: "me", Host: "box.example.org"},
		"ssh://me@10.0.0.7:2222":   {User: "me", Host: "10.0.0.7", Port: 2222},
		"ssh://me@[::1]:22":        {User: "me", Host: "::1", Port: 22},
	}
	for raw, want := range good {
		got, err := ParseURL(raw)
		if err != nil {
			t.Errorf("ParseURL(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseURL(%q) = %+v, want %+v", raw, got, want)
		}
	}

	bad := []string{
		"",
		"box",
		"tcp://box:7000",
		"http://box",
		"ssh://",
		"ssh://box/path",
		"ssh://box?x=1",
		"ssh://me:secret@box",
		// A host or user beginning with a dash would reach ssh as an option.
		"ssh://-oProxyCommand=sh@box",
		"ssh://me@-oProxyCommand=sh",
		"ssh://box:0",
		"ssh://box:70000",
		"ssh://box:22x",
		"ssh://me;rm@box",
		"ssh://bo x",
	}
	for _, raw := range bad {
		if _, err := ParseURL(raw); !errors.Is(err, ErrValidation) {
			t.Errorf("ParseURL(%q) = %v, want ErrValidation", raw, err)
		}
	}
}

func TestAHostNamePrefixesItsIDs_REQ_RMT_001(t *testing.T) {
	for _, name := range []string{"nas", "build-01", "a"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q): %v", name, err)
		}
	}
	// A name is the prefix of every id the host's daemon hands out, `name/…`, so it cannot hold
	// the separator, and it is a path component of the forwarded socket.
	for _, name := range []string{"", "Nas", "1nas", "nas/x", "nas.local", "-nas", "../x", "nas_", "abcdefghijklmnopqrstuvwxyzabcdefg"} {
		if err := ValidateName(name); !errors.Is(err, ErrValidation) {
			t.Errorf("ValidateName(%q) = %v, want ErrValidation", name, err)
		}
	}
}

func TestTheRemoteSocketMustBeAPlainPath_REQ_RMT_001(t *testing.T) {
	if err := ValidateEndpoint(Endpoint{Socket: "/run/user/1000/umbral/umbral.sock", Token: "t"}); err != nil {
		t.Fatalf("a plain endpoint: %v", err)
	}
	// ssh's -L reads `local:remote`; a colon, a relative path or a newline in what the remote
	// side printed would forward something else.
	for _, ep := range []Endpoint{
		{Socket: "relative.sock", Token: "t"},
		{Socket: "/tmp/a:b.sock", Token: "t"},
		{Socket: "/tmp/a\nb", Token: "t"},
		{Socket: "/tmp/a.sock", Token: ""},
	} {
		if err := ValidateEndpoint(ep); !errors.Is(err, ErrValidation) {
			t.Errorf("ValidateEndpoint(%+v) = %v, want ErrValidation", ep, err)
		}
	}
}

func TestBackoffDoublesToItsCap_REQ_RMT_002(t *testing.T) {
	want := []int{1, 2, 4, 8, 16, 30, 30}
	for attempt, seconds := range want {
		if got := Backoff(attempt); got.Seconds() != float64(seconds) {
			t.Errorf("Backoff(%d) = %v, want %ds", attempt, got, seconds)
		}
	}
}
