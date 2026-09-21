# Umbral — specification package (SDD v2, spec-anchored)

This folder holds all Spec-Driven Development artifacts that precede code.

**Status:** PRD 1.8, API 1.11, Technical Design 1.8, Data Model 1.6, Plan 1.4, constitution 1.2.
Every finding that blocked implementation is folded, seventeen deltas are archived and the quality
gate passes with no CRITICAL findings. **No delta is pending ratification.** What remains are
MEDIUM and LOW items tracked as issues. The specs carry both lineages: the orchestration surface of
2026-09-20 and the deltas raised while F0 was being built.

## Map

| Path | What it is |
|---|---|
| `../AGENTS.md`, `../CLAUDE.md` | instructions for agents; they point to the constitution |
| `../docs/ARCHITECTURE.md` | research (Warp, Wave, Crush, Zed/ACP) and conceptual architecture |
| `../docs/adr/ADR-0001-architectural-style.md` | architectural style decision |
| `../docs/adr/ADR-0002-orchestration-surface.md` | workspaces, waits, integrations and restore guarantees |
| `constitution.md` | 9 non-negotiable articles + stack constraints |
| `prd/umbral-mvp.md` | PRD with EARS criteria (REQ-*) for F0 + F1 |
| `api/umbral-daemon-api-v1.md` | JSON-RPC contract client ↔ `umbrald` |
| `technical/umbral-architecture.md` | Technical Design (DD-001…008) |
| `data-model/umbral-schema.md` | SQLite schema, indexes ↔ queries, retention |
| `plans/umbral-mvp-plan.md` | phased plan |
| `tasks/umbral-f0-tasks.md`, `tasks/umbral-f1-tasks.md`, `tasks/umbral-hardening-tasks.md` | executable tasks with traceability matrices |
| `analyze/analyze-2026-09-20b.md` | current validation: verdict READY TO IMPLEMENT, findings C-01…C-04 |
| `analyze/analyze-2026-09-20.md` | previous report against specs 1.1, kept as history |
| `analyze/analyze-2026-09-11.md` | previous report against specs 1.0, kept as history |
| `../changes/_archive/` | the ten folded Delta Specs, kept as the record of what changed and why |
| `../assets/branding/umbral-icons/` | icon kit (Linux/Windows/macOS), SVG sources and generators |
| `../tools/sdd_check.py` | REQ → task coverage and DDL check (CI-ready) |
| `../docs/checkpoints/` | inventories verified against the filesystem |

## Flow

1. Review and approve the artifacts in order: constitution → PRD → API → Tech Design → Data Model → Plan → Tasks.
2. First supervised batch: T-F0-01…T-F0-04, then the structure tasks T-F0-14…T-F0-18.
3. Any change to specified behaviour from here on enters as a new Delta in `changes/`.

```bash
python3 tools/sdd_check.py                                             # coverage and DDL
python3 assets/branding/umbral-icons/tools/build.py --out /tmp/icons   # regenerate icons
```
