package domain

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The layout is a binary tree of splits and panes (API Spec §4 `Layout`). A tab holds one.
//
// The JSON codec lives here rather than in `api` because this tree is not a wire encoding
// of internal state: it is the portable artifact REQ-WS-004 exports and REQ-WS-005 applies,
// and Data Model §2.4b stores that same shape in `tabs.layout_json`. One definition means
// a layout written by the store and one sent to a client cannot drift; two would have to be
// kept in step by hand. What stays in `api` is everything that makes it a *message* —
// method names, the JSON-RPC envelope, the error table.

// NodeType distinguishes the two kinds of node.
type NodeType string

// The two node types API Spec §4 defines.
const (
	NodePane  NodeType = "pane"
	NodeSplit NodeType = "split"
)

// ErrPaneNotInLayout is returned when a layout operation names a pane the tree does not
// contain. It is a distinct error because the service maps it to NOT_FOUND, while a
// malformed tree is an internal fault.
var ErrPaneNotInLayout = errors.New("workspaces: the layout has no such pane")

// Node is one position in the tree: either a pane or a split of two children.
type Node struct {
	Type NodeType `json:"type"`

	// Pane nodes.
	PaneID  string            `json:"pane_id,omitempty"`
	Label   string            `json:"label,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// Split nodes.
	Direction SplitDirection `json:"direction,omitempty"`
	Ratio     float64        `json:"ratio,omitempty"`
	First     *Node          `json:"first,omitempty"`
	Second    *Node          `json:"second,omitempty"`
}

// Layout is a tab's whole tree (API Spec §4 `Layout`).
type Layout struct {
	WorkspaceID   string `json:"workspace_id"`
	TabID         string `json:"tab_id"`
	FocusedPaneID string `json:"focused_pane_id,omitempty"`
	Root          *Node  `json:"root,omitempty"`
}

// PaneNode builds a leaf from a pane.
func PaneNode(p Pane) *Node {
	return &Node{
		Type: NodePane, PaneID: p.ID, Label: p.Label, CWD: p.CWD,
		Command: p.Command, Env: p.Env,
	}
}

// PaneIDs lists every pane in the tree, left to right, which is the order a client draws
// them in and the order `pane.list` returns.
func (n *Node) PaneIDs() []string {
	if n == nil {
		return nil
	}
	if n.Type == NodePane {
		return []string{n.PaneID}
	}
	return append(n.First.PaneIDs(), n.Second.PaneIDs()...)
}

// SplitAt replaces the leaf holding paneID with a split of that leaf and a new one.
//
// The original pane becomes `first` and the new one `second`, which is what "the new pane in
// that position" means for both directions API Spec §5.6 allows: `right` puts the new pane
// to the right of the one being split, `down` below it.
func (n *Node) SplitAt(paneID string, direction SplitDirection, ratio float64, added *Node) (*Node, error) {
	if n == nil {
		return nil, ErrPaneNotInLayout
	}
	switch n.Type {
	case NodePane:
		if n.PaneID != paneID {
			return nil, ErrPaneNotInLayout
		}
		existing := *n
		return &Node{
			Type: NodeSplit, Direction: direction, Ratio: ratio,
			First: &existing, Second: added,
		}, nil

	case NodeSplit:
		if replaced, err := n.First.SplitAt(paneID, direction, ratio, added); err == nil {
			out := *n
			out.First = replaced
			return &out, nil
		} else if !errors.Is(err, ErrPaneNotInLayout) {
			return nil, err
		}
		replaced, err := n.Second.SplitAt(paneID, direction, ratio, added)
		if err != nil {
			return nil, err
		}
		out := *n
		out.Second = replaced
		return &out, nil
	}
	return nil, fmt.Errorf("workspaces: layout node of unknown type %q", n.Type)
}

// RemovePane takes a pane out and collapses the split that held it into its sibling, which
// is what makes a closed pane give its space back rather than leave a gap.
//
// A nil result means the tree is now empty: the tab's last pane is gone.
func (n *Node) RemovePane(paneID string) (*Node, error) {
	if n == nil {
		return nil, ErrPaneNotInLayout
	}
	switch n.Type {
	case NodePane:
		if n.PaneID != paneID {
			return nil, ErrPaneNotInLayout
		}
		return nil, nil

	case NodeSplit:
		if replaced, err := n.First.RemovePane(paneID); err == nil {
			if replaced == nil {
				return n.Second, nil
			}
			out := *n
			out.First = replaced
			return &out, nil
		} else if !errors.Is(err, ErrPaneNotInLayout) {
			return nil, err
		}
		replaced, err := n.Second.RemovePane(paneID)
		if err != nil {
			return nil, err
		}
		if replaced == nil {
			return n.First, nil
		}
		out := *n
		out.Second = replaced
		return &out, nil
	}
	return nil, fmt.Errorf("workspaces: layout node of unknown type %q", n.Type)
}

// Rename updates a pane leaf's label in place in a copy of the tree, so the exported layout
// carries the name the user gave the pane.
func (n *Node) Rename(paneID, label string) (*Node, error) {
	if n == nil {
		return nil, ErrPaneNotInLayout
	}
	switch n.Type {
	case NodePane:
		if n.PaneID != paneID {
			return nil, ErrPaneNotInLayout
		}
		out := *n
		out.Label = label
		return &out, nil

	case NodeSplit:
		if replaced, err := n.First.Rename(paneID, label); err == nil {
			out := *n
			out.First = replaced
			return &out, nil
		} else if !errors.Is(err, ErrPaneNotInLayout) {
			return nil, err
		}
		replaced, err := n.Second.Rename(paneID, label)
		if err != nil {
			return nil, err
		}
		out := *n
		out.Second = replaced
		return &out, nil
	}
	return nil, fmt.Errorf("workspaces: layout node of unknown type %q", n.Type)
}

// MarshalLayout serialises a tab's layout for `tabs.layout_json`.
//
// The whole Layout goes in, not just its root, because `focused_pane_id` has nowhere else
// to live: Data Model §2.4b gives `tabs` no such column, and describes this one as holding
// the "portable tree (API Layout)" — and the API Layout is the object that carries it.
func MarshalLayout(layout Layout) ([]byte, error) {
	raw, err := json.Marshal(layout)
	if err != nil {
		return nil, fmt.Errorf("workspaces: encode layout: %w", err)
	}
	return raw, nil
}

// UnmarshalLayout reads a tab's layout back. `{}` and the empty string both mean an empty
// tab: the first is the column's own default, the second a row written before it applied.
func UnmarshalLayout(raw []byte) (Layout, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return Layout{}, nil
	}
	var layout Layout
	if err := json.Unmarshal(raw, &layout); err != nil {
		return Layout{}, fmt.Errorf("workspaces: stored layout is not readable: %w", err)
	}
	if layout.Root != nil && layout.Root.Type == "" {
		layout.Root = nil
	}
	return layout, nil
}

// ClampRatio applies API Spec §5.6's bounds: 0.1 to 0.9, defaulting to 0.5.
//
// Zero means "not given" rather than "no space", because the API makes the field optional
// and JSON cannot tell an absent number from a zero one.
func ClampRatio(ratio float64) float64 {
	if ratio == 0 {
		return DefaultSplitRatio
	}
	if ratio < MinSplitRatio {
		return MinSplitRatio
	}
	if ratio > MaxSplitRatio {
		return MaxSplitRatio
	}
	return ratio
}
