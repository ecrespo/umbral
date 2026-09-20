# Getting started: GitHub repository + development with Claude Code

Steps to turn this package into a living repository, with one issue per task and Claude Code working
task by task under Spec-Driven Development.

## 0. Requirements on your machine

| Tool | Purpose | Check |
|---|---|---|
| git + authenticated GitHub CLI (`gh`) | repo, issues, PRs | `gh auth status` |
| Python ≥ 3.10 | `tools/sdd_check.py`, bootstrap, hooks | `python3 --version` |
| Node ≥ 20 | Mermaid validator | `node --version` |
| Go ≥ 1.25 | from T-F0-01 | `go version` |
| Zig (the version libghostty requires) | building libghostty-vt (T-F0-01) | `zig version` |
| bash, zsh, fish | shell-integration tests (T-F0-08) | `which zsh fish` |
| Claude Code | assisted development | `claude --version` |
| Ollama with `gpt-oss:20b` | F1 (local models) | `ollama list` |

Installing Claude Code: <https://docs.claude.com/en/docs/claude-code/overview>.

## 1. Initialize the local repository

```bash
unzip umbral-repo.zip && cd umbral
git init -b main
npm install --prefix tools --no-audit --no-fund
python3 tools/sdd_check.py        # expected: 1 CRITICAL (A-01); the first PR fixes it
node tools/mermaid_check.mjs      # expected: all valid
git add -A
git commit -m "docs: initial Umbral specification package (SDD)"
```

`python3 tools/sdd_check.py` exits with code 1 while A-01 is open. That is why the CI **Specs** job
will be red on the first push. It is intentional: the first PR turns it green.

## 2. Create the GitHub repo and populate it

First as a dry run, to review the commands:

```bash
python3 scripts/github_bootstrap.py --repo ecrespo/umbral --dry-run --create-repo --protect-main | less
```

Then for real:

```bash
python3 scripts/github_bootstrap.py --repo ecrespo/umbral --create-repo --protect-main
```

The script is idempotent: you can run it again without duplicating anything. It does the following:

- creates the public repo and pushes `main` (add `--private` if you prefer);
- sets the description, topics, discussions and *squash merge* only;
- enables *Private Vulnerability Reporting*;
- creates 37 labels (`type:*`, `phase:*`, `area:*`, `severity:*`, `parallel`, `claude-ready`);
- creates 5 milestones: Spec fixes → F0 → F1 → Hardening 0.1 → F2;
- creates **57 task issues**, one per pending `T-…`, with their dependencies linked as `#number`;
- creates **5 epics** with checklists;
- creates one issue per open Analyze finding not covered by a delta (today 10: B-01…B-10);
- protects `main`: requires the 4 CI checks, linear history and forbids force-push. It does not require reviews, because as the only maintainer you cannot approve your own PRs.

## 3. Connect Claude Code to GitHub (for `@claude` in issues and PRs)

**Recommended option:**

1. Open `claude` inside the repo.
2. Run `/install-github-app`. It installs the GitHub App and stores the secret.
3. If it proposes its own `claude.yml`, keep only one: the repo's file is already configured.

**Manual option:**

1. Install the app from <https://github.com/apps/claude> for this repo only.
2. Run `claude setup-token` and store the token as the secret `CLAUDE_CODE_OAUTH_TOKEN` (Settings → Secrets → Actions).
3. If you prefer API billing, store `ANTHROPIC_API_KEY` and uncomment that line in `.github/workflows/claude.yml` and `claude-review.yml`.

From then on you have two workflows:

- **Claude Code**: answers `@claude` in issues, comments and reviews.
- **Claude spec review**: automatically reviews every PR against the constitution and the specs.

## 4. First local session: close the Analyze findings

```bash
cd umbral && claude
```

1. Accept trust for the directory. Without it, the `allow` rules in `.claude/settings.json` do not apply.
2. Run `/status` and check that *Setting sources* includes *Project settings*. The start hook shows the CRITICAL finding A-01 and the next tasks.
3. Approve the `context7` MCP server when prompted.
4. Enter **Plan Mode** (Shift+Tab until you see *plan mode*) and paste:

> Read AGENTS.md and `changes/2026-09-analyze-fixes/`. Review the proposal with me and, if I
> approve it, run T-FIX-01 to T-FIX-04 on the branch `spec/analyze-fixes`: fold the delta into
> `specs/`, update `tools/sdd_check.py` and leave `python3 tools/sdd_check.py` without CRITICAL
> findings. Then run `/sdd-analyze`, archive the delta in `changes/_archive/` and prepare the commit.

5. Review the changes.
6. Run the read-only reviewer:
   > use the spec-guardian subagent on this branch
7. Push and open the PR:
   ```bash
   git push -u origin spec/analyze-fixes
   gh pr create --fill --base main
   ```
8. With CI green, *squash & merge*. `main` is now clean.

## 5. Continue F0

**T-F0-01 to T-F0-10 are already done**, so `go.mod`, the Taskfile, the linters and CI exist and
`task lint && task arch && task test` is the local gate from the first clone. Before the first
build, run `task deps:ghostty` once: it builds libghostty-vt with Zig, and without it the cgo
packages do not compile. `task test` injects the `PKG_CONFIG_PATH` it needs; a bare
`go test ./...` does not.

`umb` exists from T-F0-11, so the daemon is already usable by hand:

```bash
umb status              # starts umbrald if nothing is listening
umb block last --json   # the last closed block, as the API Spec §4 Block schema
```

What is left in F0, one task per session or per branch:

- **T-F0-12** (TUI) is what makes Umbral usable as a terminal rather than as a daemon.
- **T-F0-13** adds the performance gates to CI.
- **T-F0-14 to T-F0-18** are the structure tasks — workspaces, panes, portable layouts, sequenced
  snapshots and restore — and they are what make the daemon scriptable. T-F0-14 comes first; the
  other four depend on it.

At most 3-5 tasks before a general human review. Close each batch with a checkpoint in
`docs/checkpoints/`, verified against the filesystem rather than against your own notes, and adjust
the specs through a Delta in `changes/` if something did not fit.

### Parallel work (`[P]` tasks)

Tasks marked `[P]` (for example T-F0-04 and T-F0-08) do not share files. Use worktrees for them:

```bash
git worktree add ../umbral-T-F0-08 -b feat/T-F0-08-shell-bootstrap
cd ../umbral-T-F0-08 && claude     # in another terminal: /implement-task T-F0-08
```

### Small tasks from GitHub

On the issue of a `claude-ready` task, comment:

> @claude implement T-F0-11 following AGENTS.md and .claude/skills/implement-task/SKILL.md; open a PR against main

The workflow creates the branch and the PR. You review it like any other PR.

## 6. Cadence

| When | What |
|---|---|
| Every task | `/implement-task` → `spec-guardian` → PR with the template → merge |
| Every 3-5 tasks | `/checkpoint <slug>` |
| Spec is wrong | stop → `/spec-delta <slug> "<motivation>"` → approve → fold it in the PR that implements it |
| End of a phase | `/sdd-analyze` + `/checkpoint phase-N`; close the epic and the milestone |
| Before F1 | approve and fold the visual identity delta (respecting A-12) |

## 7. Personal configuration (not committed)

Put your preferences in `.claude/settings.local.json` (model, extra permissions). Claude Code keeps
it out of git automatically when it creates the file. If you create it by hand, it is already in
`.gitignore`.

## 8. Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| The *Specs* job is red | A-01 or another CRITICAL is open | section 4 |
| Claude asks permission for everything | the directory is not trusted, or the settings did not load | `/status`, `/permissions` |
| The start hook does not show up | `python3` is not on the PATH | `/hooks` to inspect |
| `@claude` does not answer | missing secret or GitHub App | section 3; check the Actions tab |
| The *Icon kit integrity* job warns in "Rebuild" | a different cairo version on the runner | informational; only `sha256sum -c` blocks |
