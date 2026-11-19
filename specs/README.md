# Umbral — specification package (SDD v2, spec-anchored)

This folder holds the Spec-Driven Development artifacts the code is written from.

**Status on 2026-11-19:** constitution 1.6, PRD 1.24, F2 PRD 0.16, API 1.62, Technical Design
1.96, Data Model 1.28, Plan 1.15. `python3 tools/sdd_check.py` passes with no CRITICAL and no
HIGH finding: 179 of 179 MUST requirements have a task.

- **Phases:** F0 closed on 2026-09-26, F1 on 2026-10-22 and slice F2a on 2026-10-24. The tasks
  of F2b … F2f are done; closing those slices is the Tech Lead's. The Hardening phase is open.
- **Deltas:** 92 are archived under `../changes/_archive/`. 16 are open in `../changes/`, all
  from F2, approved by the implementing agent and waiting for the Tech Lead's review
  (`../docs/checkpoints/2026-11-17-delta-review-guide.md` is a reading order).
- **Open tasks:** T-PKG-05 in the F2 file; T-REL-01, T-REL-02, T-REL-03, T-HRD-01 (in
  progress) and T-HRD-13 … T-HRD-20 in the hardening file.

`../AGENTS.md`, under "Known status", is the running record; when this summary and that file
disagree, that file is the newer one.

## Map

| Path | What it is |
|---|---|
| `../AGENTS.md`, `../CLAUDE.md` | instructions for agents; they point to the constitution |
| `../docs/ARCHITECTURE.md` | research (Warp, Wave, Crush, Zed/ACP) and conceptual architecture |
| `../docs/adr/ADR-0001-architectural-style.md` | architectural style decision |
| `../docs/adr/ADR-0002-orchestration-surface.md` | workspaces, waits, integrations and restore guarantees |
| `constitution.md` | 9 non-negotiable articles, the stack constraints and their amendments |
| `prd/umbral-mvp.md` | PRD with EARS criteria (REQ-*) for F0, F1 and the release |
| `prd/umbral-f2.md` | PRD for F2: orchestration, the agent in the terminal, desktop, durability and remote hosts, Windows, extensibility |
| `api/umbral-daemon-api-v1.md` | JSON-RPC contract client ↔ `umbrald` |
| `technical/umbral-architecture.md` | Technical Design (DD-001 … DD-017) |
| `data-model/umbral-schema.md` | SQLite schema, indexes ↔ queries, retention, recovery |
| `plans/umbral-mvp-plan.md` | phased plan |
| `tasks/umbral-f0-tasks.md`, `tasks/umbral-f1-tasks.md`, `tasks/umbral-f2-tasks.md`, `tasks/umbral-hardening-tasks.md` | executable tasks with traceability matrices and execution logs |
| `analyze/analyze-2026-09-20b.md` | current validation: verdict READY TO IMPLEMENT; C-03 and C-04, both LOW, are open |
| `analyze/analyze-2026-09-20.md`, `analyze/analyze-2026-09-11b.md`, `analyze/analyze-2026-09-11.md` | earlier reports, kept as history |
| `../changes/` | the open Delta Specs |
| `../changes/_archive/` | the folded Delta Specs, kept as the record of what changed and why |
| `../docs/checkpoints/` | inventories verified against the filesystem, and each phase's closing checkpoint |
| `../docs/reports/` | the live runs of US-003, US-F2-001 and US-F2-002 |
| `../docs/qa/` | the walkthroughs a person performs: the F0 and F1 TUI, the F2 desktop |
| `../docs/runbooks/rule-signing.md` | custody of the rule-signing key |
| `../docs/user/` | user documentation: providers, data retention |
| `../assets/branding/umbral-icons/` | icon kit (Linux/Windows/macOS), SVG sources and generators |
| `../tools/sdd_check.py` | REQ → task coverage and DDL check, run by `task specs` and CI |

## Flow

1. Read in order: constitution → the task → only the spec sections the task touches
   (`../AGENTS.md`, "Mandatory reading order").
2. One task per branch and per pull request, tests first.
3. Any change to specified behaviour enters as a Delta in `changes/<YYYY-MM>-<slug>/` and is
   ratified before it is folded and archived. Code and `specs/` never diverge silently.

```bash
task specs                                                             # coverage, DDL, Mermaid, pinned tools
python3 tools/sdd_check.py                                             # coverage and DDL alone
python3 assets/branding/umbral-icons/tools/build.py --out /tmp/icons   # regenerate icons
```
