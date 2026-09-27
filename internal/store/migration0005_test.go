package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// agentTables is every object migration 0005 creates (Data Model §5), so a table or an index
// left out of the file fails here by name rather than at the first F1 query that needs it.
var agentTables = map[string][]string{
	"table": {
		"messages", "tool_calls", "approvals", "policy_rules", "models", "usage", "egress_log",
		"mcp_servers", "pane_state_reports", "pane_metadata", "trust_keys", "rule_bundles", "skills",
	},
	"index": {
		"idx_messages_thread_created", "idx_messages_client_msg", "idx_tool_calls_thread",
		"idx_approvals_pending", "idx_policy_rules_lookup", "idx_usage_model_created",
		"idx_usage_thread", "idx_egress_created", "idx_pane_metadata_expiry",
		"idx_rule_bundles_active",
	},
}

// seedAgentParents inserts the rows every agent table points at: a thread, a message, a tool
// call, a session with a block, and a workspace, tab and pane.
func seedAgentParents(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO threads(id, cwd, created_at, updated_at) VALUES ('thr_a', '/repo', 1, 1)`,
		`INSERT INTO threads(id, cwd, created_at, updated_at) VALUES ('thr_b', '/repo', 1, 1)`,
		`INSERT INTO messages(id, thread_id, turn_id, role, content, created_at)
		 VALUES ('msg_parent', 'thr_a', 'turn_1', 'assistant', 'x', 1)`,
		`INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at)
		 VALUES ('tc_parent', 'thr_a', 'msg_parent', 'run_command', 'Exec', '{}', 'pending', 1)`,
		`INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at)
		 VALUES ('tc_second', 'thr_a', 'msg_parent', 'edit_file', 'WriteFS', '{}', 'pending', 1)`,
		`INSERT INTO workspaces(id, label, cwd, created_at) VALUES ('w1', 'api', '/repo', 1)`,
		`INSERT INTO tabs(id, workspace_id, label, created_at) VALUES ('w1:t1', 'w1', 'main', 1)`,
		`INSERT INTO panes(id, tab_id, cwd, created_at) VALUES ('w1:p1', 'w1:t1', '/repo', 1)`,
		`INSERT INTO trust_keys(id, public_key, fingerprint, added_at) VALUES ('key_1', x'00', 'fp1', 1)`,
	} {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("seed %q: %v", strings.Fields(stmt)[2], err)
		}
	}
}

// TestMigration0005Constraints: the agent subdomain's tables exist, with the indexes §3 maps
// queries onto, and every CHECK, foreign key and unique index the Data Model writes into
// their DDL refuses what it is there to refuse and accepts what it is there to allow. A
// constraint the migration dropped would let a bad row in today and fail a query in F1.
func TestMigration0005Constraints(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	db := s.DB()

	for kind, names := range agentTables {
		for _, name := range names {
			var got string
			err := db.QueryRowContext(t.Context(),
				"SELECT name FROM sqlite_master WHERE type = ? AND name = ?", kind, name).Scan(&got)
			if err != nil {
				t.Errorf("migration 0005 did not create %s %s: %v", kind, name, err)
			}
		}
	}

	// The partial indexes stay partial. For the unique one the WHERE changes nothing SQLite
	// would refuse — NULLs never collide in a unique index — so only its definition can say
	// that the index covers the rows its lookup reads and nothing else.
	for index, where := range map[string]string{
		"idx_messages_client_msg":  "WHERE client_msg_id IS NOT NULL",
		"idx_approvals_pending":    "WHERE state = 'pending'",
		"idx_pane_metadata_expiry": "WHERE expires_at IS NOT NULL",
		"idx_rule_bundles_active":  "WHERE active = 1",
	} {
		var ddl string
		if err := db.QueryRowContext(t.Context(),
			"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&ddl); err != nil {
			t.Errorf("read %s: %v", index, err)
			continue
		}
		if !strings.Contains(ddl, where) {
			t.Errorf("%s is %q, want it partial: %s", index, ddl, where)
		}
	}

	seedAgentParents(t, db)

	for _, tc := range []struct {
		name string
		stmt string
		ok   bool
	}{
		// messages (§2.6)
		{"a message", `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at)
			VALUES ('msg_1', 'thr_a', 't', 'user', 'hi', 1)`, true},
		{"a message id without its prefix", `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at)
			VALUES ('m_1', 'thr_a', 't', 'user', 'hi', 1)`, false},
		{"a message of an unknown role", `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at)
			VALUES ('msg_2', 'thr_a', 't', 'robot', 'hi', 1)`, false},
		{"a message of a missing thread", `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at)
			VALUES ('msg_3', 'thr_none', 't', 'user', 'hi', 1)`, false},
		{"tainted is a flag", `INSERT INTO messages(id, thread_id, turn_id, role, content, tainted, created_at)
			VALUES ('msg_4', 'thr_a', 't', 'tool', 'x', 2, 1)`, false},
		{"a client id", `INSERT INTO messages(id, thread_id, turn_id, role, content, client_msg_id, created_at)
			VALUES ('msg_5', 'thr_a', 't', 'user', 'hi', 'c1', 1)`, true},
		{"the same client id twice in a thread (REQ-AGT-015)", `INSERT INTO messages(id, thread_id, turn_id, role, content, client_msg_id, created_at)
			VALUES ('msg_6', 'thr_a', 't', 'user', 'hi', 'c1', 1)`, false},
		{"the same client id in another thread", `INSERT INTO messages(id, thread_id, turn_id, role, content, client_msg_id, created_at)
			VALUES ('msg_7', 'thr_b', 't', 'user', 'hi', 'c1', 1)`, true},
		{"many messages without a client id (A-04)", `INSERT INTO messages(id, thread_id, turn_id, role, content, created_at)
			VALUES ('msg_8', 'thr_a', 't', 'user', 'hi', 1)`, true},

		// tool_calls (§2.7)
		{"a tool call id without its prefix", `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at)
			VALUES ('call_1', 'thr_a', 'msg_parent', 'x', 'Exec', '{}', 'pending', 1)`, false},
		{"a tool call of an unknown risk", `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at)
			VALUES ('tc_1', 'thr_a', 'msg_parent', 'x', 'Dangerous', '{}', 'pending', 1)`, false},
		{"a tool call of an unknown status", `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at)
			VALUES ('tc_2', 'thr_a', 'msg_parent', 'x', 'Exec', '{}', 'maybe', 1)`, false},
		{"a tool call of a missing message", `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, started_at)
			VALUES ('tc_3', 'thr_a', 'msg_none', 'x', 'Exec', '{}', 'pending', 1)`, false},
		{"a tool call of a missing block", `INSERT INTO tool_calls(id, thread_id, message_id, tool, risk, args_json, status, block_id, started_at)
			VALUES ('tc_4', 'thr_a', 'msg_parent', 'x', 'Exec', '{}', 'pending', 'blk_none', 1)`, false},

		// approvals (§2.8)
		{"an approval", `INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, created_at)
			VALUES ('apr_1', 'thr_a', 'tc_parent', 'run_command', 'Exec', 'destructive', 'rm -rf build', 1)`, true},
		{"a second approval for the same tool call", `INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, created_at)
			VALUES ('apr_2', 'thr_a', 'tc_parent', 'run_command', 'Exec', 'policy', 's', 1)`, false},
		{"an approval of an unknown reason", `INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, created_at)
			VALUES ('apr_3', 'thr_a', 'tc_second', 'edit_file', 'WriteFS', 'whim', 's', 1)`, false},
		{"an approval in an unknown state", `INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, state, created_at)
			VALUES ('apr_4', 'thr_a', 'tc_second', 'edit_file', 'WriteFS', 'policy', 's', 'lost', 1)`, false},
		{"an approval with an unknown scope", `INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, decision_scope, created_at)
			VALUES ('apr_5', 'thr_a', 'tc_second', 'edit_file', 'WriteFS', 'policy', 's', 'forever', 1)`, false},
		{"an approval id without its prefix", `INSERT INTO approvals(id, thread_id, tool_call_id, tool, risk, reason, summary, created_at)
			VALUES ('ap_6', 'thr_a', 'tc_second', 'edit_file', 'WriteFS', 'policy', 's', 1)`, false},

		// policy_rules (§2.9)
		{"a policy rule", `INSERT INTO policy_rules(tool, decision, source, created_at)
			VALUES ('run_command', 'allow', 'config', 1)`, true},
		{"a policy rule that is neither allow nor deny", `INSERT INTO policy_rules(tool, decision, source, created_at)
			VALUES ('run_command', 'ask', 'config', 1)`, false},
		{"a policy rule from an unknown source", `INSERT INTO policy_rules(tool, decision, source, created_at)
			VALUES ('run_command', 'deny', 'model', 1)`, false},

		// models, usage, egress_log (§2.10-2.12; REQ-LLM-005, REQ-SEC-002)
		{"a model", `INSERT INTO models(id, provider, local, caps_json, context_window, updated_at)
			VALUES ('ollama/gpt-oss:20b', 'ollama', 1, '{}', 131072, 1)`, true},
		{"a model of an unknown health", `INSERT INTO models(id, provider, local, caps_json, context_window, health, updated_at)
			VALUES ('x/y', 'x', 0, '{}', 1, 'fine', 1)`, false},
		{"a usage row (REQ-LLM-005)", `INSERT INTO usage(thread_id, model_id, provider, status, in_tokens, out_tokens, first_token_ms, cost_micro_usd, created_at)
			VALUES ('thr_a', 'ollama/gpt-oss:20b', 'ollama', 'ok', 10, 20, 150, 0, 1)`, true},
		{"a usage row of an unknown status", `INSERT INTO usage(model_id, provider, status, created_at)
			VALUES ('m', 'p', 'meh', 1)`, false},
		{"an egress row (REQ-SEC-002)", `INSERT INTO egress_log(thread_id, provider, host, bytes, payload_sha256, created_at)
			VALUES ('thr_a', 'openrouter', 'openrouter.ai', 1024, '` + strings.Repeat("a", 64) + `', 1)`, true},
		{"an egress row with a short digest", `INSERT INTO egress_log(provider, host, bytes, payload_sha256, created_at)
			VALUES ('p', 'h', 1, 'abc', 1)`, false},

		// mcp_servers (§2.13)
		{"a stdio MCP server", `INSERT INTO mcp_servers(id, name, transport, command, created_at, updated_at)
			VALUES ('mcp_1', 'gitlab', 'stdio', 'gitlab-mcp', 1, 1)`, true},
		{"a stdio MCP server with no command", `INSERT INTO mcp_servers(id, name, transport, created_at, updated_at)
			VALUES ('mcp_2', 'fs', 'stdio', 1, 1)`, false},
		{"an http MCP server with no url", `INSERT INTO mcp_servers(id, name, transport, created_at, updated_at)
			VALUES ('mcp_3', 'web', 'http', 1, 1)`, false},
		{"a second MCP server of the same name", `INSERT INTO mcp_servers(id, name, transport, command, created_at, updated_at)
			VALUES ('mcp_4', 'gitlab', 'stdio', 'x', 1, 1)`, false},
		{"an MCP server name starting in upper case", `INSERT INTO mcp_servers(id, name, transport, command, created_at, updated_at)
			VALUES ('mcp_5', 'GitLab', 'stdio', 'x', 1, 1)`, false},
		{"an MCP server id without its prefix", `INSERT INTO mcp_servers(id, name, transport, command, created_at, updated_at)
			VALUES ('srv_6', 'other', 'stdio', 'x', 1, 1)`, false},

		// pane_state_reports and pane_metadata (§2.4c)
		{"a state report", `INSERT INTO pane_state_reports(pane_id, source, state, updated_at)
			VALUES ('w1:p1', 'claude-code', 'working', 1)`, true},
		{"a state report of an unknown state", `INSERT INTO pane_state_reports(pane_id, source, state, updated_at)
			VALUES ('w1:p1', 'other', 'sleeping', 1)`, false},
		{"a state report for a missing pane", `INSERT INTO pane_state_reports(pane_id, source, state, updated_at)
			VALUES ('w9:p9', 'x', 'idle', 1)`, false},
		{"a metadata key", `INSERT INTO pane_metadata(pane_id, source, key, value, updated_at)
			VALUES ('w1:p1', 'x', 'branch', 'main', 1)`, true},
		{"an empty metadata key", `INSERT INTO pane_metadata(pane_id, source, key, value, updated_at)
			VALUES ('w1:p1', 'x', '', 'v', 1)`, false},
		{"a metadata key over 32 characters", `INSERT INTO pane_metadata(pane_id, source, key, value, updated_at)
			VALUES ('w1:p1', 'x', '` + strings.Repeat("k", 33) + `', 'v', 1)`, false},
		{"a metadata value over 80 characters", `INSERT INTO pane_metadata(pane_id, source, key, value, updated_at)
			VALUES ('w1:p1', 'x', 'long', '` + strings.Repeat("v", 81) + `', 1)`, false},

		// trust_keys and rule_bundles (§2.4e)
		{"a second key with the same fingerprint", `INSERT INTO trust_keys(id, public_key, fingerprint, added_at)
			VALUES ('key_2', x'01', 'fp1', 1)`, false},
		{"an active rule bundle", `INSERT INTO rule_bundles(version, sha256, source, verified_with, installed_at, active)
			VALUES (1, 'h', 'remote', 'key_1', 1, 1)`, true},
		{"a second active rule bundle", `INSERT INTO rule_bundles(version, sha256, source, installed_at, active)
			VALUES (2, 'h', 'builtin', 1, 1)`, false},
		{"an inactive rule bundle beside it", `INSERT INTO rule_bundles(version, sha256, source, installed_at)
			VALUES (3, 'h', 'local', 1)`, true},
		{"a rule bundle from an unknown source", `INSERT INTO rule_bundles(version, sha256, source, installed_at)
			VALUES (4, 'h', 'usb', 1)`, false},
		{"a rule bundle verified with a missing key", `INSERT INTO rule_bundles(version, sha256, source, verified_with, installed_at)
			VALUES (5, 'h', 'remote', 'key_none', 1)`, false},

		// skills (§2.4f)
		{"a skill", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_1', 'pdf-tools', 'Reads PDFs.', '/src', 'h', 100, 1)`, true},
		{"a skill id without its prefix", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('sk_2', 'other', 'd', '/src', 'h', 1, 1)`, false},
		{"a second skill of the same name", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_3', 'pdf-tools', 'd', '/src', 'h', 1, 1)`, false},
		{"a skill name with upper case", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_4', 'Pdf', 'd', '/src', 'h', 1, 1)`, false},
		{"a skill name starting with a hyphen", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_5', '-pdf', 'd', '/src', 'h', 1, 1)`, false},
		{"a skill name with an underscore", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_6', 'pdf_tools', 'd', '/src', 'h', 1, 1)`, false},
		{"a skill name over 64 characters", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_7', '` + strings.Repeat("a", 65) + `', 'd', '/src', 'h', 1, 1)`, false},
		{"a skill with an empty description", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_8', 'empty', '', '/src', 'h', 1, 1)`, false},
		{"a skill over 8 MiB", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_9', 'huge', 'd', '/src', 'h', 8388609, 1)`, false},
		{"an empty skill", `INSERT INTO skills(id, name, description, source, sha256, size_bytes, installed_at)
			VALUES ('skl_10', 'empty-bundle', 'd', '/src', 'h', 0, 1)`, false},
	} {
		_, err := db.ExecContext(t.Context(), tc.stmt)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case !tc.ok && err == nil:
			t.Errorf("%s: accepted; the Data Model's DDL refuses it", tc.name)
		}
	}

	// Defaults that are decisions, not conveniences: an MCP server starts untrusted
	// (Art. 5) and a message starts untainted until something taints it (REQ-SEC-006).
	for _, check := range []struct{ query, want string }{
		{`SELECT trust FROM mcp_servers WHERE id = 'mcp_1'`, "untrusted"},
		{`SELECT tainted FROM messages WHERE id = 'msg_1'`, "0"},
		{`SELECT attention_state FROM threads WHERE id = 'thr_a'`, "idle"},
	} {
		var got string
		if err := db.QueryRowContext(t.Context(), check.query).Scan(&got); err != nil || got != check.want {
			t.Errorf("%s = %q (%v), want %q", check.query, got, err, check.want)
		}
	}

	// A thread's conversation goes with it, and its cost audit does not (REQ-LLM-005): the
	// usage row stays, with no thread to point at.
	if _, err := db.ExecContext(t.Context(), `DELETE FROM approvals; DELETE FROM tool_calls WHERE thread_id = 'thr_a'`); err != nil {
		t.Fatalf("clear the thread's tool calls: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM threads WHERE id = 'thr_a'`); err != nil {
		t.Fatalf("delete the thread: %v", err)
	}
	var messages, orphanUsage int
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM messages WHERE thread_id = 'thr_a'`).Scan(&messages); err != nil || messages != 0 {
		t.Errorf("%d messages outlived their thread (%v); want a cascade", messages, err)
	}
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM usage WHERE thread_id IS NULL AND model_id = 'ollama/gpt-oss:20b'`).Scan(&orphanUsage); err != nil || orphanUsage != 1 {
		t.Errorf("the usage row did not survive its thread with thread_id NULL (%d, %v)", orphanUsage, err)
	}

	// Cascades: a pane's reports and metadata go with it.
	if _, err := db.ExecContext(t.Context(), `DELETE FROM panes WHERE id = 'w1:p1'`); err != nil {
		t.Fatalf("delete the pane: %v", err)
	}
	for _, table := range []string{"pane_state_reports", "pane_metadata"} {
		var n int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s holds %d rows after its pane was deleted (%v); want a cascade", table, n, err)
		}
	}
}

// TestMigration0005AddsTheAttentionColumnsToExistingThreads: migration 0001 created
// `threads` and is applied everywhere, so REQ-AGT-016's columns arrive by ALTER, and a thread
// that existed before 0005 ran has to come out of it with the defaults (Data Model §2.5).
func TestMigration0005AddsTheAttentionColumnsToExistingThreads(t *testing.T) {
	t.Parallel()

	available, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "umbral.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	// A database that stopped at 0004, holding a thread.
	for _, m := range available {
		if m.version > 4 {
			break
		}
		if _, err := raw.ExecContext(t.Context(), m.sql); err != nil {
			t.Fatalf("apply %s: %v", m.name, err)
		}
		if _, err := raw.ExecContext(t.Context(),
			"INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)", m.version); err != nil {
			t.Fatalf("record %s: %v", m.name, err)
		}
	}
	if _, err := raw.ExecContext(t.Context(),
		`INSERT INTO threads(id, cwd, created_at, updated_at) VALUES ('thr_old', '/repo', 1, 1)`); err != nil {
		t.Fatalf("insert a thread at 0004: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatalf("upgrade past 0004: %v", err)
	}
	defer func() { _ = s.Close() }()

	var attention string
	var seen sql.NullInt64
	if err := s.DB().QueryRowContext(t.Context(),
		`SELECT attention_state, seen_at FROM threads WHERE id = 'thr_old'`).Scan(&attention, &seen); err != nil {
		t.Fatalf("read the attention columns: %v", err)
	}
	if attention != "idle" || seen.Valid {
		t.Errorf("an existing thread came out with attention_state = %q, seen_at = %v; want idle and NULL",
			attention, seen)
	}
}
