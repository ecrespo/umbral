package domain

import "strings"

// The `pane_metadata` keys a pane's display metadata is stored under (Data Model §2.4c,
// delta `2026-10-display-metadata`). The integration surface writes them; the tree reads them
// back into Metadata.
const (
	MetaKeyTitle       = "title"
	MetaKeyDisplayName = "display_name"
	MetaKeyLabelPrefix = "label:"
	MetaKeyTokenPrefix = "token:"
	// MetaKeySeq is the reserved row holding a source's last metadata seq for the pane; it is
	// never displayed. internal/store/retention.go names it literally to exempt it from the
	// age purge; rename both together.
	MetaKeySeq = "#seq"
)

// Metadata is what integrations display about a pane (REQ-INT-004, API Spec §4 `metadata`).
// It is display only: ResolveAttention and the rollup never read it.
type Metadata struct {
	Title       string
	DisplayName string
	// StateLabels maps a state to the label a source shows for it; Tokens is free key-value
	// text. Both are nil when empty.
	StateLabels map[string]string
	Tokens      map[string]string
}

// MetadataFromKeys folds a pane's live rows, oldest first, into Metadata: when two sources
// set one key, the later write wins.
func MetadataFromKeys(rows [][2]string) Metadata {
	var m Metadata
	for _, row := range rows {
		key, value := row[0], row[1]
		switch {
		case key == MetaKeyTitle:
			m.Title = value
		case key == MetaKeyDisplayName:
			m.DisplayName = value
		case strings.HasPrefix(key, MetaKeyLabelPrefix):
			if m.StateLabels == nil {
				m.StateLabels = map[string]string{}
			}
			m.StateLabels[strings.TrimPrefix(key, MetaKeyLabelPrefix)] = value
		case strings.HasPrefix(key, MetaKeyTokenPrefix):
			if m.Tokens == nil {
				m.Tokens = map[string]string{}
			}
			m.Tokens[strings.TrimPrefix(key, MetaKeyTokenPrefix)] = value
		}
	}
	return m
}
