// Package domain is the notification sink's model: what a component may ask to show, how its
// text is normalized, and the typed reasons it is answered with (REQ-NTF-001, REQ-NTF-002,
// API Spec §5.35).
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrValidation is a request that cannot be shown however the daemon is configured: an empty
// title, a malformed source or an unknown sound. internal/api maps it to VALIDATION_ERROR.
var ErrValidation = errors.New("notify: validation")

// The bounds of REQ-NTF-001 and REQ-NTF-002.
const (
	MaxTitleLen  = 80
	MaxBodyLen   = 240
	MaxSourceLen = 80
	// RateLimit requests per source fit in any RateWindow; the excess is discarded.
	RateLimit  = 5
	RateWindow = 60 * time.Second
	// ReservedPrefix names the daemon's own sources. A client claiming one would be
	// spending the budget of a component it is not.
	ReservedPrefix = "umbral:"
)

// Sound is API Spec §5.35's `sound`.
type Sound string

// The three sounds a client may play. The daemon plays nothing; it only forwards the hint.
const (
	SoundNone    Sound = "none"
	SoundDone    Sound = "done"
	SoundRequest Sound = "request"
)

// Reason is API Spec §5.35's typed answer.
type Reason string

// The four reasons of REQ-NTF-001.
const (
	ReasonShown       Reason = "shown"
	ReasonDisabled    Reason = "disabled"
	ReasonRateLimited Reason = "rate_limited"
	ReasonNoClient    Reason = "no_client"
)

// Request is what a component asks to show, as it arrived.
type Request struct {
	Title  string
	Body   string
	Source string
	Sound  Sound
}

// Notification is a request once normalized, which is what reaches a client.
type Notification struct {
	Title  string
	Body   string
	Source string
	Sound  Sound
}

// Result is what the caller is told.
type Result struct {
	Shown  bool
	Reason Reason
}

// Build normalizes a request and refuses one that cannot be shown, including one whose
// source claims the reserved prefix.
func (r Request) Build() (Notification, error) {
	if strings.HasPrefix(r.Source, ReservedPrefix) {
		return Notification{}, fmt.Errorf("%w: source %q is reserved for Umbral's own components", ErrValidation, r.Source)
	}
	if err := validateSource(r.Source); err != nil {
		return Notification{}, err
	}
	sound := r.Sound
	switch sound {
	case "":
		sound = SoundNone
	case SoundNone, SoundDone, SoundRequest:
	default:
		return Notification{}, fmt.Errorf("%w: sound %q is not one of none, done and request", ErrValidation, sound)
	}
	n := Notification{
		Title: Normalize(r.Title, MaxTitleLen), Body: Normalize(r.Body, MaxBodyLen),
		Source: r.Source, Sound: sound,
	}
	if n.Title == "" {
		return Notification{}, fmt.Errorf("%w: the title is empty once control characters are removed", ErrValidation)
	}
	return n, nil
}

// validateSource is API Spec §5.31's rule for a source, which a notification shares so one
// hook can use one name for its reports and its notifications.
func validateSource(source string) error {
	if source == "" || len(source) > MaxSourceLen {
		return fmt.Errorf("%w: source must be 1 to %d characters", ErrValidation, MaxSourceLen)
	}
	for _, c := range source {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == ':' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("%w: source %q may hold only A-Z, a-z, 0-9, ':', '.', '_' and '-'", ErrValidation, source)
		}
	}
	return nil
}

// Normalize is REQ-NTF-001's rule: control and Unicode format characters removed — so an
// escape sequence loses its ESC and shows as text — whitespace runs collapsed to one space,
// the ends trimmed, and the result cut at limit characters rather than refused.
func Normalize(s string, limit int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	space := false
	for _, c := range s {
		switch {
		case unicode.IsSpace(c):
			space = b.Len() > 0
			continue
		case unicode.IsControl(c), unicode.Is(unicode.Cf, c):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(c)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > limit {
		out = strings.TrimRight(string([]rune(out)[:limit]), " ")
	}
	return out
}
