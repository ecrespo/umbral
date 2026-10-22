package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	wsdomain "github.com/ecrespo/umbral/internal/workspaces/domain"
)

// Display metadata (REQ-INT-004, API Spec §5.32): what a pane shows about itself, kept apart
// from its semantic state on purpose. Nothing here is read by DD-012's precedence, the
// rollup, a wait or a notification.

// The bounds of API Spec §5.32 and Data Model §2.4c.
const (
	MaxMetadataValueLen = 80
	MaxTokensPerReport  = 16
	MaxLiveKeysPerPane  = 32
	MaxTTLMillis        = 86_400_000
	// MaxTokenNameLen keeps `token:<name>` within the column's 32 characters.
	MaxTokenNameLen = 32 - len(KeyTokenPrefix)
)

// The keys a report's fields are stored under in `pane_metadata`, which the tree reads back.
// KeySeq is the reserved row holding a source's last metadata seq for the pane: never
// displayed, and counted toward no limit.
const (
	KeyTitle       = wsdomain.MetaKeyTitle
	KeyDisplayName = wsdomain.MetaKeyDisplayName
	KeyLabelPrefix = wsdomain.MetaKeyLabelPrefix
	KeyTokenPrefix = wsdomain.MetaKeyTokenPrefix
	KeySeq         = wsdomain.MetaKeySeq
)

// Labelable are the states a source may give a display label (`state_labels`).
var Labelable = map[string]bool{
	"idle": true, "working": true, "blocked": true, "done": true, "unknown": true,
}

// MetadataReport is one `pane.report_metadata`. A nil pointer, or a nil entry, means the
// field was sent as `null` and clears its key; a field not sent is absent from Set and Clear
// alike, and stays as it was.
type MetadataReport struct {
	PaneID string
	Source string
	// Set is every key the report writes, normalized; Clear every key it removes, and
	// ClearPrefixes every kind of key (KeyLabelPrefix, KeyTokenPrefix) it removes whole, for a
	// `null` object.
	Set           map[string]string
	Clear         []string
	ClearPrefixes []string
	// TTLMillis bounds the life of every key the report sets; 0 is MaxTTLMillis, the most any
	// key lives (Data Model §4).
	TTLMillis int64
	Seq       *int64
}

// MetadataFields is a report as the wire carries it, before it becomes keys.
type MetadataFields struct {
	PaneID, Source string
	// Title and DisplayName are nil for `null`; TitleSent and DisplayNameSent say whether they
	// were sent at all, so a `null` clears and an absent field stays as it was.
	Title, DisplayName         *string
	TitleSent, DisplayNameSent bool
	StateLabels, Tokens        map[string]*string
	// StateLabelsNull and TokensNull say the object was sent as `null`: every label or token
	// of the source is cleared.
	StateLabelsNull, TokensNull bool
	// TTLMillis is nil when not sent: the keys then live MaxTTLMillis.
	TTLMillis *int64
	Seq       *int64
}

// Build validates the fields and turns them into keys (API Spec §5.32).
func (f MetadataFields) Build() (MetadataReport, error) {
	if err := ValidatePane(f.PaneID); err != nil {
		return MetadataReport{}, err
	}
	if err := ValidateSource(f.Source); err != nil {
		return MetadataReport{}, err
	}
	var ttl int64
	if f.TTLMillis != nil {
		if ttl = *f.TTLMillis; ttl < 1 || ttl > MaxTTLMillis {
			return MetadataReport{}, fmt.Errorf("%w: ttl_ms must be 1 to %d", ErrValidation, MaxTTLMillis)
		}
	}
	if f.Seq != nil && *f.Seq < 0 {
		return MetadataReport{}, fmt.Errorf("%w: seq %d is negative", ErrValidation, *f.Seq)
	}
	if len(f.Tokens) > MaxTokensPerReport {
		return MetadataReport{}, fmt.Errorf("%w: at most %d tokens per report, got %d",
			ErrValidation, MaxTokensPerReport, len(f.Tokens))
	}
	r := MetadataReport{PaneID: f.PaneID, Source: f.Source, Set: map[string]string{}, TTLMillis: ttl, Seq: f.Seq}
	put := func(key string, value *string) {
		if value == nil {
			r.Clear = append(r.Clear, key)
			return
		}
		// A value that normalizes to nothing has nothing to display: it clears, like null.
		if v := NormalizeDisplay(*value); v != "" {
			r.Set[key] = v
		} else {
			r.Clear = append(r.Clear, key)
		}
	}
	if f.TitleSent {
		put(KeyTitle, f.Title)
	}
	if f.DisplayNameSent {
		put(KeyDisplayName, f.DisplayName)
	}
	for state, label := range f.StateLabels {
		if !Labelable[state] {
			return MetadataReport{}, fmt.Errorf("%w: state_labels key %q is not a state", ErrValidation, state)
		}
		put(KeyLabelPrefix+state, label)
	}
	for name, value := range f.Tokens {
		if err := validTokenName(name); err != nil {
			return MetadataReport{}, err
		}
		put(KeyTokenPrefix+name, value)
	}
	if f.StateLabelsNull {
		r.ClearPrefixes = append(r.ClearPrefixes, KeyLabelPrefix)
	}
	if f.TokensNull {
		r.ClearPrefixes = append(r.ClearPrefixes, KeyTokenPrefix)
	}
	if len(r.Set) == 0 && len(r.Clear) == 0 && len(r.ClearPrefixes) == 0 {
		return MetadataReport{}, fmt.Errorf("%w: a metadata report needs title, display_name, state_labels or tokens", ErrValidation)
	}
	return r, nil
}

func validTokenName(name string) error {
	if name == "" || len(name) > MaxTokenNameLen {
		return fmt.Errorf("%w: a token name must be 1 to %d characters", ErrValidation, MaxTokenNameLen)
	}
	for _, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("%w: token name %q may hold only A-Z, a-z, 0-9, '.', '_' and '-'", ErrValidation, name)
		}
	}
	return nil
}

// NormalizeDisplay is §5.32's normalization: control characters removed, whitespace collapsed
// to one space and trimmed, then cut to 80 characters. A control character that is also
// whitespace (tab, newline, carriage return) counts as whitespace. An escape sequence loses its ESC and
// shows as text, never as a command to the terminal that draws it.
func NormalizeDisplay(s string) string {
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
	if utf8.RuneCountInString(out) > MaxMetadataValueLen {
		out = string([]rune(out)[:MaxMetadataValueLen])
		out = strings.TrimRight(out, " ")
	}
	return out
}
