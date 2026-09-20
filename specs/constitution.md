# Constitution — Umbral

> Version 1.1 · Ratified: pending (proposed 2026-09-11) · Last amendment: 2026-09-20 (Art. 5)
> Scope: the `umbral` repository (daemon `umbrald`, clients `umbral-tui`, `umbral-desktop`, CLI `umb`)
> SDD rigor level: **spec-anchored** — `specs/` is the current truth; changes enter through `changes/`.

Every agent (human or AI) working in this repo loads this file first. `AGENTS.md` and `CLAUDE.md`
point here.

## Articles

### Art. 1 — Code quality
THE TEAM SHALL keep the pre-commit and CI gate green. The gate is made of `gofumpt`, `go vet`,
`golangci-lint` (with `gosec` enabled), `govulncheck` and `gitleaks`. No merge happens with hooks
disabled or with a `//nolint` lacking a justification comment.
*Rationale: an automated gate stops style and known-vulnerability debates from happening in every PR.*

### Art. 2 — Traceable tests
THE SYSTEM SHALL cover every `MUST` requirement with at least one automated test whose name or
comment cites its ID (`REQ-XXX-NNN`). `go test -race ./...` SHALL pass before any task is closed.
*Rationale: REQ → task → test traceability answers "why does this code exist?". The race detector is
mandatory in a daemon full of goroutines.*

### Art. 3 — Architecture boundaries
THE SYSTEM SHALL respect the dependency rules in Technical Design §4, which CI verifies with
`go-arch-lint`:

- `domain` imports nothing outside the standard library;
- modules talk to each other only through published ports or bus events;
- only `cmd/umbrald` does the wiring.

A new import cycle or a layer violation breaks CI.
*Rationale: a modular monolith without verified boundaries degenerates into a big ball of mud.*

### Art. 4 — Local-first and privacy
THE SYSTEM SHALL work end to end without network access, using at least one local provider
(Ollama, llama.cpp or LM Studio). Every request that leaves the machine SHALL go through secret
redaction and SHALL be recorded in `egress_log`. There SHALL be no remote telemetry unless the user
enables it explicitly.
*Rationale: this is the product's core promise against terminals that require a cloud account.*

### Art. 5 — Agent safety
THE SYSTEM SHALL submit every tool with risk `WriteFS`, `Exec` or `Network` to a policy-engine
decision (`allow` / `ask` / `deny`), with `ask` as the default. The `full-auto` mode SHALL only run
inside a sandbox or a disposable worktree. Secrets SHALL be read from the operating system keyring,
and only from an environment variable when the keyring is unavailable, the user has enabled that
fallback explicitly, and the daemon reports it as a degraded mode (amendment of 2026-09-20).
Rule material that arrives over the network SHALL be signed and verified before use.
*Rationale: an agent with a shell is an attack surface (prompt injection, exfiltration, deletion).
Explicit consent is the primary control.*

### Art. 6 — Data
THE SYSTEM SHALL apply these data rules:

- timestamps as **UTC epoch milliseconds** (`INTEGER`);
- monetary costs as **micro-USD in `int64`** (never `float`);
- public identifiers as **type-prefixed ULIDs** (`ses_`, `blk_`, `thr_`, …);
- versioned, forward-only SQLite migrations.

*Rationale: no rounding errors in costs, no time-zone ambiguity and no ID collisions.*

### Art. 7 — Observability
THE SYSTEM SHALL emit one OpenTelemetry trace per agent turn, with spans for each model call and
each tool, plus JSON `slog` logs that include the `trace_id`. No log line SHALL contain secrets or
redacted content in clear text.
*Rationale: without traces nobody can answer "why did the agent do X?".*

### Art. 8 — Open protocols and versioned contracts
THE SYSTEM SHALL be extended through open protocols:

- MCP for tools;
- ACP for external agents;
- OpenAI- / Anthropic-compatible APIs for models.

The client-daemon API SHALL declare its version in the handshake and stay compatible within a major
version.
*Rationale: it avoids the lock-in the product criticizes in other ADEs and protects clients from
silent changes.*

### Art. 9 — Process
THE TEAM SHALL follow these process rules:

- get an approved spec before implementing any work of size "medium feature" or larger;
- channel changes to already-specified behavior through a Delta Spec in `changes/`;
- update `specs/` as part of the Definition of Done;
- validate every Mermaid diagram before accepting it;
- after multi-step generation, close with a checkpoint verified against the filesystem.

*Rationale: code and spec must never diverge silently.*

## Stack constraints

| Area | Fixed decision |
|---|---|
| Language | Go ≥ 1.25 (uses `iter`, `log/slog`) |
| VT emulation | libghostty-vt through `go.mitchellh.com/libghostty`, behind the `Emulator` port |
| PTY | `creack/pty` (Unix); ConPTY through `aymanbagabas/go-pty` once Windows is enabled |
| Persistence | SQLite with `modernc.org/sqlite`, WAL mode, FTS5 |
| Client-daemon protocol | JSON-RPC 2.0 over Unix socket / named pipe |
| LLM | own `llm.Provider` port; `charm.land/fantasy` as the main adapter |
| MCP | `github.com/modelcontextprotocol/go-sdk` |
| TUI | Bubble Tea v2 + Lip Gloss |
| Desktop | Wails v3 (phase F2) |
| CI | GitHub Actions or GitLab CI, with the Art. 1 gate |
| MVP platforms | Linux (x86_64, arm64) and macOS (arm64); Windows from F2 |

## Amendments

| Date | Article | Change | Reason | Approved by |
|---|---|---|---|---|
| 2026-09-20 | 5 | Secrets may come from an environment variable when the keyring is unavailable, the fallback is enabled explicitly and the state is reported as degraded | Headless Linux, SSH sessions and containers have no Secret Service; without this, remote providers are unusable there and the workaround would be plaintext configuration, which is worse | Ernesto Crespo |
| 2026-09-20 | 5 | Rule material fetched over the network must carry a valid signature | The redaction rules and the destructive-pattern list are the product's security control; an unsigned update can silently disable it | Ernesto Crespo |

## Constitution check (use in every artifact)

At the end of every PRD, Technical Design and Plan there is a 3-5 line section. It lists which
articles apply and how they are met, or which exception is requested and why. An exception without
a written justification is a violation.
