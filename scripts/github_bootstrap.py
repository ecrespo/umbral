#!/usr/bin/env python3
"""Bootstrap the Umbral GitHub repository from the SDD artifacts.

Creates (idempotently) labels, milestones, one issue per task in specs/tasks/ and
changes/*/tasks.md, epic issues per milestone, and issues for open Analyze findings
not already covered by a delta. Optionally creates the repo and protects `main`.

Requires the GitHub CLI (`gh auth login`). No third-party Python packages.

Usage:
  python3 scripts/github_bootstrap.py --repo ecrespo/umbral --dry-run
  python3 scripts/github_bootstrap.py --repo ecrespo/umbral --create-repo --protect-main
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

DESCRIPTION = "Local-first agentic terminal in Go: command blocks, a permissioned agent and local models (Ollama, llama.cpp, LM Studio). MCP + ACP."
TOPICS = ["go", "golang", "terminal", "terminal-emulator", "ai-agents", "agentic", "llm", "ollama",
          "llama-cpp", "mcp", "acp", "spec-driven-development", "libghostty", "bubbletea"]

LABELS = {
    "type:task": ("1d76db", "Atomic task from specs/tasks"),
    "type:bug": ("d73a4a", "Behavior that contradicts the spec"),
    "type:spec": ("5319e7", "Change in specs/ or changes/"),
    "type:epic": ("3e4b9e", "Phase tracking"),
    "sdd:delta": ("8a63d2", "Delta Spec"),
    "analyze:finding": ("fbca04", "Analyze finding"),
    "severity:critical": ("b60205", "Blocks implementation"),
    "severity:high": ("d93f0b", "Gap an agent would fill by guessing"),
    "severity:medium": ("fef2c0", "Friction or drift"),
    "severity:low": ("c2e0c6", "Style"),
    "parallel": ("0e8a16", "[P] task: parallelizable"),
    "claude-ready": ("f9d0c4", "Spec detailed enough for an agent to implement"),
    "phase:F0": ("bfdadc", "Terminal core"),
    "phase:F1": ("bfd4f2", "Agentic (MVP)"),
    "phase:hardening": ("d4c5f9", "Release 0.1"),
    "phase:F2": ("c5def5", "ADE"),
    "phase:spec-fixes": ("e99695", "Spec fixes before F0"),
}
for area in ["sessions", "agents", "context", "tools", "llmgw", "mcp", "security", "store", "api",
             "obs", "tui", "cli", "config", "packaging", "ci", "specs",
             "workspaces", "waits", "integrations", "notify"]:
    LABELS[f"area:{area}"] = ("ededed", f"Module {area}")

MILESTONES = {
    "spec-fixes": ("Spec fixes · Analyze 2026-09-11", "Fold A-01…A-07 before T-F0-02 (changes/2026-09-analyze-fixes)."),
    "F0": ("F0 · Core", "Daemon, PTY, libghostty, blocks, search, base TUI and CLI."),
    "F1": ("F1 · Agentic (MVP)", "Permissioned agent, model gateway, MCP, security and OTel."),
    "hardening": ("Hardening · 0.1", "Packaging, retention, recovery and release 0.1."),
    "F2": ("F2 · ADE", "Desktop client and ADE features."),
}

AREA_RULES = [
    (r"internal/(sessions|agents|context|tools|llmgw|mcp|security|store|api|obs|config|workspaces|waits|integrations|notify)\b", None),
    (r"cmd/umbral-tui|internal/tui", "tui"),
    (r"cmd/umb\b|cmd/umb/", "cli"),
    (r"shell/|testdata/vt", "sessions"),
    (r"\.github/|Taskfile|\.golangci|go-arch-lint", "ci"),
    (r"assets/branding|\.desktop|\.deb|Wails|wails", "packaging"),
    (r"specs/|tools/sdd_check", "specs"),
]


@dataclass
class Task:
    tid: str
    title: str
    body: str
    source: str
    milestone: str
    parallel: bool
    deps: list[str] = field(default_factory=list)
    areas: set[str] = field(default_factory=set)


def run(cmd: list[str], dry: bool, capture: bool = False) -> str:
    print("$ " + " ".join(cmd if len(" ".join(cmd)) < 220 else cmd[:6] + ["…"]))
    if dry:
        return ""
    res = subprocess.run(cmd, check=True, text=True, capture_output=capture)
    return res.stdout if capture else ""


def milestone_for(source: Path, tid: str) -> str:
    name = source.as_posix()
    if "analyze-fixes" in name:
        return "spec-fixes"
    if "visual-identity" in name:
        return "F2" if tid in {"T-PKG-04", "T-PKG-05"} else "hardening"
    if "f0-tasks" in name:
        return "F0"
    if "f1-tasks" in name:
        return "F1"
    return "F1"


def parse_tasks() -> list[Task]:
    files = sorted((ROOT / "specs/tasks").glob("*.md"))
    files += sorted(p for p in (ROOT / "changes").glob("*/tasks.md") if "_archive" not in p.parts)
    tasks: list[Task] = []
    for f in files:
        text = f.read_text(encoding="utf-8")
        for block in re.split(r"\n### ", text)[1:]:
            m = re.match(r"\[(.)\] (?:\d{4}-\d{2}-\d{2} )?(T-[A-Z0-9]+-\d{2}) · (.+)", block)
            if not m:
                continue
            if m.group(1) == "x":
                continue  # already done
            tid, raw_title = m.group(2), m.group(3).strip()
            body = block.split("\n", 1)[1].split("\n## ", 1)[0].strip()
            deps_line = re.search(r"\*\*Depends on:\*\* (.*)", body)
            deps = re.findall(r"T-[A-Z0-9]+-\d{2}", deps_line.group(1)) if deps_line else []
            files_line = re.search(r"\*\*Files:\*\* (.*)", body)
            areas: set[str] = set()
            src_text = (files_line.group(1) if files_line else "") + " " + raw_title
            for pattern, fixed in AREA_RULES:
                for hit in re.finditer(pattern, src_text):
                    areas.add(fixed or hit.group(1))
            rel = f.relative_to(ROOT).as_posix()
            if "analyze-fixes" in rel:
                areas.add("specs")
            if "visual-identity" in rel:
                areas.add("packaging")
            tasks.append(Task(tid, raw_title.replace("[P] ", ""), body, rel,
                              milestone_for(f, tid), "[P]" in raw_title, deps, areas))
    return tasks


def parse_open_findings(covered: set[str]) -> list[dict]:
    reports = sorted((ROOT / "specs/analyze").glob("analyze-*.md"))
    if not reports:
        return []
    rows = re.findall(r"^\| ([A-Z]-\d{2}) \| \**([A-Z]+)\** \| ([^|]+) \| ([^|]+) \| ([^|]+) \| ([^|]+) \|$",
                      reports[-1].read_text(encoding="utf-8"), re.M)
    sev = {"CRITICAL": "critical", "HIGH": "high", "MEDIUM": "medium", "LOW": "low"}
    out = []
    for aid, severity, cat, finding, artifacts, suggestion in rows:
        if aid in covered:
            continue
        out.append({"id": aid, "severity": sev.get(severity, "medium"), "category": cat.strip(),
                    "finding": finding.strip(), "artifacts": artifacts.strip(),
                    "suggestion": suggestion.strip(), "report": reports[-1].relative_to(ROOT).as_posix()})
    return out


def covered_findings() -> set[str]:
    covered: set[str] = set()
    for p in (ROOT / "changes").glob("*/proposal.md"):
        if "_archive" in p.parts:
            continue
        covered |= set(re.findall(r"[A-Z]-\d{2}", p.read_text(encoding="utf-8").split("**Scope", 1)[0]))
    if (ROOT / "changes/2026-09-analyze-fixes").exists():
        covered |= {f"A-{i:02d}" for i in range(1, 8)}
    return covered


def task_issue_body(t: Task, numbers: dict[str, int], repo: str) -> str:
    deps = ", ".join(f"#{numbers[d]} ({d})" if d in numbers else d for d in t.deps) or "—"
    return (
        f"> Source: [`{t.source}`](https://github.com/{repo}/blob/main/{t.source}) · "
        f"generated by `scripts/github_bootstrap.py`\n\n"
        f"{t.body}\n\n---\n**Dependencies (issues):** {deps}\n\n"
        f"**With Claude Code (local):** `/implement-task {t.tid}`\n\n"
        f"**With Claude Code on GitHub:** comment `@claude implement {t.tid} following AGENTS.md "
        f"and .claude/skills/implement-task/SKILL.md; open a PR against main`.\n"
    )


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--repo", required=True, help="owner/name, e.g. ecrespo/umbral")
    ap.add_argument("--dry-run", action="store_true", help="print the gh commands without running them")
    ap.add_argument("--create-repo", action="store_true", help="create the repo from this directory and push")
    ap.add_argument("--private", action="store_true", help="with --create-repo: private instead of public")
    ap.add_argument("--protect-main", action="store_true", help="require CI checks and forbid force-push on main")
    ap.add_argument("--skip-issues", action="store_true")
    args = ap.parse_args()
    dry, repo = args.dry_run, args.repo

    if not dry:
        try:
            subprocess.run(["gh", "auth", "status"], check=True, capture_output=True)
        except (FileNotFoundError, subprocess.CalledProcessError):
            print("gh CLI missing or not authenticated: run `gh auth login`.", file=sys.stderr)
            return 2

    # 1. Repository
    if args.create_repo:
        vis = "--private" if args.private else "--public"
        run(["gh", "repo", "create", repo, vis, "--source", str(ROOT), "--remote", "origin",
             "--push", "--description", DESCRIPTION], dry)
    run(["gh", "repo", "edit", repo, "--description", DESCRIPTION, "--enable-issues", "--enable-discussions",
         "--enable-wiki=false", "--delete-branch-on-merge", "--enable-squash-merge",
         "--enable-merge-commit=false", "--enable-rebase-merge=false"]
        + [x for t in TOPICS for x in ("--add-topic", t)], dry)
    run(["gh", "api", "-X", "PUT", f"repos/{repo}/private-vulnerability-reporting"], dry)

    # 2. Labels
    for name, (color, desc) in LABELS.items():
        run(["gh", "label", "create", name, "--repo", repo, "--color", color,
             "--description", desc, "--force"], dry)

    # 3. Milestones
    existing_ms: dict[str, int] = {}
    if not dry:
        data = json.loads(run(["gh", "api", f"repos/{repo}/milestones?state=all&per_page=100"], dry, True) or "[]")
        existing_ms = {m["title"]: m["number"] for m in data}
    for key, (title, desc) in MILESTONES.items():
        if title not in existing_ms:
            run(["gh", "api", f"repos/{repo}/milestones", "-f", f"title={title}", "-f", f"description={desc}"], dry)

    if not args.skip_issues:
        tasks = parse_tasks()
        existing: dict[str, int] = {}
        if not dry:
            data = json.loads(run(["gh", "issue", "list", "--repo", repo, "--state", "all", "--limit", "1000",
                                   "--json", "number,title"], dry, True) or "[]")
            for it in data:
                if it["title"].startswith("Epic: "):
                    existing[it["title"]] = it["number"]
                    continue
                m = re.match(r"(T-[A-Z0-9]+-\d{2}|A-\d{2})( ·|$)", it["title"])
                if m:
                    existing[m.group(1)] = it["number"]

        # 4. Task issues (first pass: create)
        numbers: dict[str, int] = dict(existing)
        for t in tasks:
            if t.tid in numbers:
                continue
            labels = ["type:task", f"phase:{t.milestone}", "claude-ready"] + [f"area:{a}" for a in sorted(t.areas)]
            if t.parallel:
                labels.append("parallel")
            if t.milestone == "spec-fixes":
                labels += ["type:spec", "sdd:delta"]
            out = run(["gh", "issue", "create", "--repo", repo, "--title", f"{t.tid} · {t.title}",
                       "--body", task_issue_body(t, {}, repo), "--milestone", MILESTONES[t.milestone][0]]
                      + [x for lab in labels for x in ("--label", lab)], dry, True)
            if out:
                numbers[t.tid] = int(out.strip().rsplit("/", 1)[-1])

        # 5. Second pass: resolve dependencies to issue numbers
        if not dry:
            for t in tasks:
                if t.deps and t.tid in numbers:
                    run(["gh", "issue", "edit", str(numbers[t.tid]), "--repo", repo,
                         "--body", task_issue_body(t, numbers, repo)], dry)

        # 6. Epics per milestone
        for key, (title, desc) in MILESTONES.items():
            epic_key = f"Epic: {title}"
            members = [t for t in tasks if t.milestone == key]
            if not members or epic_key in existing:
                continue
            checklist = "\n".join(f"- [ ] #{numbers[t.tid]} {t.tid} · {t.title}" if t.tid in numbers
                                  else f"- [ ] {t.tid} · {t.title}" for t in members)
            run(["gh", "issue", "create", "--repo", repo, "--title", epic_key, "--label", "type:epic",
                 "--milestone", title, "--body", f"{desc}\n\n{checklist}\n"], dry)

        # 7. Open Analyze findings not covered by a delta
        for f in parse_open_findings(covered_findings()):
            if f["id"] in existing:
                continue
            body = (f"**Category:** {f['category']}\n\n**Finding:** {f['finding']}\n\n"
                    f"**Artifacts:** {f['artifacts']}\n\n**Suggestion:** {f['suggestion']}\n\n"
                    f"Source: `{f['report']}`. Resolve with `/spec-delta`.")
            run(["gh", "issue", "create", "--repo", repo, "--title", f"{f['id']} · {f['finding'][:80]}",
                 "--label", "analyze:finding", "--label", f"severity:{f['severity']}", "--label", "type:spec",
                 "--milestone", MILESTONES["spec-fixes"][0], "--body", body], dry)

    # 8. Branch protection (solo maintainer: checks required, no mandatory reviews)
    if args.protect_main:
        payload = {
            "required_status_checks": {"strict": True, "contexts": [
                "Specs (SDD coverage + Mermaid)", "Secrets (gitleaks)", "Icon kit integrity",
                "Go (vet, test -race, govulncheck)"]},
            "enforce_admins": False,
            "required_pull_request_reviews": None,
            "restrictions": None,
            "required_linear_history": True,
            "allow_force_pushes": False,
            "allow_deletions": False,
        }
        print(f"$ gh api -X PUT repos/{repo}/branches/main/protection --input - <<< '{json.dumps(payload)[:80]}…'")
        if not dry:
            subprocess.run(["gh", "api", "-X", "PUT", f"repos/{repo}/branches/main/protection", "--input", "-"],
                           input=json.dumps(payload), text=True, check=True)

    print("done" + (" (dry run)" if dry else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
