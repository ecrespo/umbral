# CLAUDE.md — Umbral

@AGENTS.md

## Claude Code specifics

- **Project skills** (in `.claude/skills/`):
  - `/implement-task <ID>`: full flow for one task;
  - `/spec-delta <slug> "<motivation>"`: spec change proposal;
  - `/sdd-analyze`: read-only cross-artifact validation gate;
  - `/checkpoint <slug>`: verification against the disk.
- **Subagents** (in `.claude/agents/`):
  - `spec-guardian`: read-only review against the constitution; use it before every task commit;
  - `test-author`: tests from EARS criteria.
- **Hooks:**
  - on session start, the project status is printed (blocking findings, next tasks, pending deltas);
  - after editing a `.go` file, `gofumpt` runs.
- **Permissions:**
  - require confirmation: the constitution and the base specs (PRD, API, technical, data, plan), `.github/`, `.claude/`, `git push` and `go get`;
  - forbidden: `rm -rf`, `push --force` and reading `.env` files or keys.
- **Recommended way of working:**
  - start each task in **Plan Mode**;
  - approve the plan;
  - let Claude implement it with `/implement-task`;
  - run `spec-guardian` before the commit.
- **MCP:** `context7` is declared in `.mcp.json` for up-to-date library docs (libghostty, Bubble Tea v2, Fantasy, MCP Go SDK).
