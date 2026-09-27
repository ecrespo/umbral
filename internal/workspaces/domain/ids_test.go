package domain

import "testing"

// TestIdentifierGrammar_REQ_WS_002 pins the shape REQ-WS-002 specifies and the Art. 6
// amendment authorises. The rejections matter as much as the acceptances: these are the
// only identifiers in Umbral that are not prefixed ULIDs, so the grammar is the whole of
// what keeps them from becoming free-form strings.
func TestIdentifierGrammar_REQ_WS_002(t *testing.T) {
	t.Parallel()

	t.Run("workspace", func(t *testing.T) {
		t.Parallel()
		good := map[string]int{"w1": 1, "w9": 9, "w10": 10, "w123": 123}
		for id, want := range good {
			if n, ok := ParseWorkspaceID(id); !ok || n != want {
				t.Errorf("ParseWorkspaceID(%q) = %d, %v; want %d, true", id, n, ok, want)
			}
		}
		// `w0` and `w01` are refused although migration 0003's GLOB accepts them: SQLite
		// has no regular expressions, so the database cannot forbid a leading zero, and
		// `w01` and `w1` being two names for one workspace is worse than being strict.
		for _, id := range []string{"", "w", "w0", "w01", "W1", "w1 ", " w1", "w1:t1", "w1:p1", "wx", "w-1", "w1.5"} {
			if _, ok := ParseWorkspaceID(id); ok {
				t.Errorf("ParseWorkspaceID(%q) was accepted", id)
			}
		}
	})

	t.Run("tab", func(t *testing.T) {
		t.Parallel()
		ws, n, ok := ParseTabID("w2:t13")
		if !ok || ws != "w2" || n != 13 {
			t.Errorf(`ParseTabID("w2:t13") = %q, %d, %v; want "w2", 13, true`, ws, n, ok)
		}
		for _, id := range []string{"w1:t0", "w1:t", "w0:t1", "w1:p1", "w1t1", "w1:T1", "w1:t1:p1", ":t1"} {
			if _, _, ok := ParseTabID(id); ok {
				t.Errorf("ParseTabID(%q) was accepted", id)
			}
		}
	})

	t.Run("pane", func(t *testing.T) {
		t.Parallel()
		ws, n, ok := ParsePaneID("w2:p7")
		if !ok || ws != "w2" || n != 7 {
			t.Errorf(`ParsePaneID("w2:p7") = %q, %d, %v; want "w2", 7, true`, ws, n, ok)
		}
		for _, id := range []string{"w1:p0", "w1:p", "w0:p1", "w1:t1", "w1p1", "w1:P1", ":p1"} {
			if _, _, ok := ParsePaneID(id); ok {
				t.Errorf("ParsePaneID(%q) was accepted", id)
			}
		}
	})

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()
		ws := FormatWorkspaceID(3)
		if n, ok := ParseWorkspaceID(ws); !ok || n != 3 {
			t.Fatalf("FormatWorkspaceID(3) = %q, which does not parse back", ws)
		}
		tab := FormatTabID(ws, 4)
		if owner, n, ok := ParseTabID(tab); !ok || owner != ws || n != 4 {
			t.Errorf("FormatTabID(%q, 4) = %q, which parses to %q, %d, %v", ws, tab, owner, n, ok)
		}
		pane := FormatPaneID(ws, 5)
		if owner, n, ok := ParsePaneID(pane); !ok || owner != ws || n != 5 {
			t.Errorf("FormatPaneID(%q, 5) = %q, which parses to %q, %d, %v", ws, pane, owner, n, ok)
		}
	})

	t.Run("WorkspaceOf answers for all three", func(t *testing.T) {
		t.Parallel()
		for id, want := range map[string]string{"w4": "w4", "w4:t2": "w4", "w4:p9": "w4"} {
			if got, ok := WorkspaceOf(id); !ok || got != want {
				t.Errorf("WorkspaceOf(%q) = %q, %v; want %q, true", id, got, ok, want)
			}
		}
		if _, ok := WorkspaceOf("ses_01M2ZT68SCQR0EKDRGTXQ2MB35"); ok {
			t.Error("WorkspaceOf accepted a session id")
		}
	})
}

// TestPaneIdentifiersAreScopedToTheWorkspace pins the choice that makes REQ-WS-007 work:
// pane numbers belong to the workspace, not to the tab. A pane dragged between two tabs of
// one workspace keeps its name and needs no alias; only a move that crosses a workspace
// boundary renames it, which is exactly the case REQ-WS-007 describes.
func TestPaneIdentifiersAreScopedToTheWorkspace(t *testing.T) {
	t.Parallel()

	pane := FormatPaneID("w1", 2)
	if pane != "w1:p2" {
		t.Fatalf("FormatPaneID = %q, want w1:p2", pane)
	}
	owner, _, ok := ParsePaneID(pane)
	if !ok {
		t.Fatal("the pane id does not parse")
	}
	// The identifier names a workspace and a pane; no tab appears in it, so moving the pane
	// to another tab of w1 cannot change its name.
	if owner != "w1" {
		t.Errorf("pane %q reports owner %q, want w1", pane, owner)
	}
	for _, tab := range []string{"w1:t1", "w1:t2"} {
		if ws, _, ok := ParseTabID(tab); !ok || ws != owner {
			t.Errorf("tab %q is not in the pane's workspace", tab)
		}
	}
}
