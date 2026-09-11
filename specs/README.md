# Umbral — specification package (SDD v2, spec-anchored)

This folder holds all Spec-Driven Development artifacts that precede code.

**Status:** every artifact is `DRAFT` / "in review". The 2026-09-11 Analyze asks to fix 1 critical
finding before implementing (`specs/analyze/`).

## Map

| Path | What it is |
|---|---|
| `../AGENTS.md`, `../CLAUDE.md` | instructions for agents; they point to the constitution |
| `../docs/ARCHITECTURE.md` | research (Warp, Wave, Crush, Zed/ACP) and conceptual architecture |
| `../docs/adr/ADR-0001-architectural-style.md` | architectural style decision |
| `constitution.md` | 9 non-negotiable articles + stack constraints |
| `prd/umbral-mvp.md` | PRD with EARS criteria (REQ-*) for F0 + F1 |
| `api/umbral-daemon-api-v1.md` | JSON-RPC contract client ↔ `umbrald` |
| `technical/umbral-architecture.md` | Technical Design (DD-001…008) |
| `data-model/umbral-schema.md` | SQLite schema, indexes ↔ queries, retention |
| `plans/umbral-mvp-plan.md` | phased plan |
| `tasks/umbral-f0-tasks.md`, `tasks/umbral-f1-tasks.md` | executable tasks with traceability matrices |
| `analyze/analyze-2026-09-11.md` | cross-artifact validation (findings and verdict) |
| `../changes/` | Delta Specs: visual identity (REQ-PKG-*) and Analyze fixes |
| `../assets/branding/umbral-icons/` | icon kit (Linux/Windows/macOS), SVG sources and generators |
| `../tools/sdd_check.py` | REQ → task coverage and DDL check (CI-ready) |
| `../docs/checkpoints/` | inventories verified against the filesystem |

## Flow

1. Review and approve the artifacts in order: constitution → PRD → API → Tech Design → Data Model → Plan → Tasks.
2. Fix the Analyze CRITICAL findings and run it again.
3. First supervised batch: T-F0-01…T-F0-04.
4. Fold the visual identity delta (respecting A-12) once approved.

```bash
python3 tools/sdd_check.py                                             # coverage and DDL
python3 assets/branding/umbral-icons/tools/build.py --out /tmp/icons   # regenerate icons
```
