# Tasks — delta `2026-09-cli-mcp`

### [ ] T-F1-37 · `umb mcp`, and the agent panel's extensions view
- **What:**
  - `umb mcp add|list|remove`, with `--stdio -- <command>`, `--http <url>`,
    `--env NAME=keyring:<path>` and `--json`; always `untrusted`; plaintext `--env` refused in
    the CLI;
  - API §2's `cli` row gains the three `mcp.server.*` methods; §5.27 gains `remove`'s and
    `list`'s params;
  - `--env` accepts `keyring:` and `env:` references and refuses anything else;
  - the agent panel lists MCP servers with their live state and skills with enabled or not, and
    for each turn the MCP tools called and the skills loaded;
  - ratification bookkeeping: PRD, the F1 tasks file and plan rows.
- **REQ:** REQ-CLI-008, REQ-TUI-004
- **Files:** `cmd/umb/**`, `internal/api/system.go`, `internal/tui/agent/**`,
  `specs/api/umbral-daemon-api-v1.md`, `specs/technical/umbral-architecture.md`,
  `specs/prd/umbral-mvp.md`, `specs/tasks/umbral-f1-tasks.md`, `specs/plans/umbral-mvp-plan.md`
- **Depends on:** T-F1-17, T-F1-20, T-F1-35
- **Done:** `TestMcpCommandsMirrorTheMethods_REQ_CLI_008`, `TestCliMayManageMcpServers_REQ_CLI_008`
  and `TestAgentPanelShowsServersAndSkills_REQ_TUI_004` green.
- **Estimate:** 1.5d

## Traceability matrix (delta)

| REQ | Tasks | Verification |
|---|---|---|
| REQ-CLI-008 | T-F1-37 | TestMcpCommandsMirrorTheMethods_REQ_CLI_008, TestCliMayManageMcpServers_REQ_CLI_008 |
| REQ-TUI-004 | T-F1-37 | TestAgentPanelShowsServersAndSkills_REQ_TUI_004 |
