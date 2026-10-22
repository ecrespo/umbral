# Architecture

Why the daemon is shaped the way it is. The normative text lives in `specs/`; these sections only record the reasoning an agent needs before touching a boundary.

## Thin clients, daemon owns state

Anything that survives closing a window lives in `umbrald`; clients render bytes and send input (DD-001).

A TUI, the `umb` CLI and the future desktop app therefore never hold authoritative state, and a fix that stores state in a client is a design violation even when it works.

## Enforced module boundaries

Every module under `internal/` splits into `domain`, `ports` and `adapters`, and `.go-arch-lint.yml` enforces the Tech Design §5.2 rules on every CI run.

`scripts/arch_selftest.sh` injects a forbidden import and asserts it is rejected, because a misconfigured rules file passes silently. Only `cmd/umbrald` wires modules together.

## Bus instead of imports

`sessions` and `llmgw` may never import `agents`, so they talk through the typed pub/sub in [[internal/bus/bus.go#Bus]].

The bus drops events for a subscriber that falls behind and counts the loss rather than blocking ([[internal/bus/bus.go#Bus#Publish]]): a stalled TUI must never stall the goroutine draining a PTY.

## Wire format stays in api

Only `internal/api` knows JSON-RPC; modules return sentinel errors and [[internal/api/errors.go#toWire]] maps them to codes.

A handler returning a numeric code has leaked the protocol into the domain. Only `INTERNAL_ERROR` carries a trace id, and its message is replaced so internals never reach a client.

## Persist before notifying

The agent runtime writes to SQLite in the turn's own goroutine before publishing on the bus (DD-007), so a client never sees an event the store cannot replay.

## Notifications reach one client

[[internal/notify/notify.go#Service]] is a sink: it normalizes, rate-limits per source and hands the result to one client through a `Deliverer` that [[internal/api/notifications.go#Server#DeliverForeground]] implements (delta `2026-10-notifications`).

Only the foreground client — the `tui` or `desktop` connection that last sent a request — is told, so two open windows do not notify twice and a CLI never receives one. A request counts against its source even when nobody receives it, so a hook looping with no TUI open cannot burst the moment one connects.

An external `pane.report_state` raises a notification only when it makes the pane `blocked`, or changes which source decides a blocked pane ([[internal/integrations/integrations.go#Service#blockedNotice]]). It is raised under the reporter's own source, so a chatty hook spends its own budget, and a repeated or stale `blocked` stays silent.

Delivery never waits on a client: the sink and `integrations` call out only after releasing their locks, and each interactive connection drains a small queue with its own writer ([[internal/api/notifications.go#conn#writePosted]]). A TUI that stops reading costs a `no_client` answer instead of stalling every other source.

The daemon's own components notify under the reserved `umbral:` prefix through [[internal/notify/notify.go#Service#ShowOwn]], with the same normalization and per-source limit; a client claiming that prefix is refused, so it cannot spend a daemon component's budget.

A hook raises one with `umb notify` ([[cmd/umb/notify.go#cmdNotify]]), which exits 0 even when the notification was not shown: `rate_limited` or `no_client` is an answer about the user, not a failure of the hook.

## Explaining a decision uses the turn's engine

[[internal/security/explain.go#Explainer#Explain]] answers `policy.explain` by calling the same `Decide` a turn calls, with the thread's mode, rules and write root (delta `2026-10-policy-explain`).

A second implementation could drift from the real policy and explain decisions the daemon never makes. For the same reason the explainer refuses a risk the registered tool does not declare, shapes a file target with the built-ins' own [[internal/tools/adapters/builtin/builtin.go#PolicyPath]], and shares one [[cmd/umbrald/policy.go#isRepo]] with the runtime.

## One rule set for every decision and redaction

[[cmd/umbrald/rules.go#loadRules]] builds the rules in force once at start: the built-in ones with the files of `rules/` beside `config.toml` on top (delta `2026-10-rule-overrides`).

Layers stack by name in [[internal/security/domain/ruleset.go#Layer]]: a same-named entry replaces the base's and a new one adds to it, so an override can replace a rule but never remove one. The base is the built-in set, or a verified bundle that replaces it whole. An invalid file is skipped whole with a warning naming it.

The agent runtime and `policy.explain` read the policy from the same [[internal/security/adapters/rules/active.go#Active]], and every redaction reads its redactor, so an explanation cannot describe a policy the daemon does not apply. [[internal/security/adapters/rules/active.go#Active#SetBase]] swaps that base and keeps the local layer on top.

The opt-in fetch in [[internal/security/adapters/rules/fetch.go#Fetcher]] writes every request, and every redirect hop, to `egress_log` before sending it, refuses a configured URL the redactor would change, and hands what it fetched to the bundle manager, never applying it itself.

### Signed bundles and recovery

[[internal/security/rules.go#Rules]] decides what a fetched bundle does, and is the only place that changes the active set's base (delta `2026-10-signed-rule-bundles`).

[[internal/security/rules.go#Rules#Offer]] verifies with [[internal/security/domain/bundle.go#VerifyBundle]] against the trust store, refuses a downgrade, and remembers a rejected digest so it is never retried. The rejection, its count and an `umbral-rules:rejected:<reason>` egress row are one transaction, written before the metric and the bus event, as DD-007 asks. A verified bundle is stored with its bytes before the base changes, so a restart reloads exactly what was applied and checks it against its digest. The version floor is the higher of the active version and the highest ever applied, so a reset cannot be undone by replaying an older signed bundle; only a revocation lowers it, because a stolen key's huge version must not lock legitimate bundles out.

A rejection also reaches a person: [[cmd/umbrald/notify.go#notifyRuleRejections]] turns each `rules.update_rejected` into a notification under the daemon's own `umbral:rules` source through [[internal/notify/notify.go#Service#ShowOwn]], subscribed before the manager starts so the first check's verdict is not lost.

Updates fail closed after three consecutive rejections, or when the trust store ends up without a valid key once it has held one; only [[internal/security/rules.go#Rules#AddKey]] reopens them, never the embedded seed, so a compromised build cannot silently restart updates the user stopped. [[internal/security/rules.go#Rules#Rollback]] and [[internal/security/rules.go#Rules#Reset]] need no key and no network, because losing the signing key must degrade the update channel and never the product; each remembers the digest it left so the next check does not undo it.

The operator reaches the manager through `rules.*` in [[internal/api/rules.go]] and `umb rules` in [[cmd/umb/rules.go#cmdRules]]. Adding or rotating a key needs `--confirm` with the key's fingerprint typed back; without it the CLI prints the fingerprint and calls nothing, which is REQ-SEC-014's explicit confirmation. [[tools/rulesign/main.go]] signs and verifies bundles with the same domain function the daemon uses, so a bundle the tool accepts is one the daemon accepts.

Storage goes through the `RuleStore` port, implemented over migration 0006 by [[internal/security/adapters/rules/store.go#Store]], which keeps `cmd/umbrald` the only place that wires SQLite to the manager.

## Waits are inventoried and stalls only inform

Every wait is registered before it subscribes, so a leaking script hits the 32-per-connection limit as an error, never a dropped connection; a stall is announced, never acted on (DD-017).

[[internal/waits/waits.go#Service]] admits a wait under its connection's count and a `wt_` id from [[internal/store/ids.go#PrefixWait]] before it costs a bus subscription, and [[internal/waits/waits.go#Service#Cancel]] ends only the waiter: the turn, command or pane it watched goes on.

Stall detection sits in the agent runtime because it already sees every event of its turns. [[internal/agents/stall.go#Runtime#observe]] runs inside `publish`, before the lossy bus, so backpressure cannot erase activity. [[internal/agents/stall.go#Runtime#watchStalls]] marks a turn idle past its window once and never cancels it, because a local model may take minutes; a turn awaiting approval is waiting for the user and never stalls, whatever its pane writes meanwhile. The window is the local one whenever [[internal/llmgw/router.go#Router#Local]] finds a local candidate.

`sessions` and `agents` cannot import each other, so [[cmd/umbrald/stall.go#followThreadOutput]] turns the output of a thread's own session into [[internal/agents/stall.go#Runtime#NoteOutput]].

External reporters are limited in [[internal/integrations/rate.go#Service#overRate]]: 10 kept reports per source per sliding second, state and metadata together, checked after validation and the pane's resolution so an unknown or agent-owned pane still answers its error. The excess answers like a kept report so a hook never fails for being chatty, and only written reports use up the window ([[internal/integrations/rate.go#Service#kept]]), so a flooding source is heard again as soon as it slows down.

## Skills are text, installed exactly as inspected

A skill is a directory of text for Umbral's own agent; the store copies it and executes nothing, and installs only the bundle whose digest the user saw (delta `2026-10-skill-store`).

[[internal/context/domain/skill.go#BundleBuilder]] enforces the per-file, per-bundle and entry limits as each entry is read, so a tarball bomb stops at the first byte over, and [[internal/context/domain/skill.go#IsRemoteSource]] refuses a URL or remote before any path is resolved: network material must be signed (Art. 5), and nothing signs skills yet.

[[internal/context/domain/skill.go#Digest]] covers file paths, sizes and contents only, so a directory and its tarball give the same digest and `skill.install` can refuse a bundle that changed after `skill.inspect`.

[[internal/context/adapters/skills/skills.go#Service]] writes through a staging directory renamed into place inside the row's transaction, with modes forced to `0644`/`0755`; [[internal/context/adapters/skills/skills.go#Service#Recover]] runs at start, from [[cmd/umbrald/skills.go#newSkills]], and removes every directory no row records — names starting with `.` cannot be skills, so staging and set-aside copies are covered by that one rule.

Because that sweep deletes, the store first claims its directory with a `.umbral-skills` marker and refuses a non-empty one without it. A replace interrupted between its renames is settled by digest: the copy at the skill's name stays only if it matches the row, else the set-aside copy comes back.

The user reaches the store through `umb skill` in [[cmd/umb/skill.go#cmdSkill]]. Install inspects, shows the manifest and installs exactly that digest, only after a `y` on a terminal or `--yes` ([[cmd/umb/skill.go#confirmInstall]]). [[cmd/umb/skill.go#sourcePath]] leaves a remote source unresolved so the daemon's refusal sees it as typed, and [[cmd/umb/skill.go#printable]] escapes every control rune of the bundle's own text, so the manifest a user confirms cannot hide a file.

The agent sees skills through [[internal/context/adapters/skills/skills.go#Service#Catalog]], read for every turn so a change reaches the next one: names and the start of each description, never a body (delta `2026-10-skill-catalog`). [[internal/context/domain/skill.go#Catalog]] folds each description to one line, because a bundle's text could otherwise open a fake section of the system prompt. It reads a body only through [[internal/tools/adapters/builtin/skill.go#skillLoad]], whose every result taints the turn, so a skill's instructions cannot run a command without the user's approval. `umb skill install|enable|remove` and `umb mcp add` are destructive patterns in [[internal/security/domain/policy_destructive.go#DefaultDestructivePatterns]], so the agent cannot install what instructs it.

## MCP servers from the CLI are untrusted, and the panel shows what the agent reaches

`umb mcp add` always sends `trust: "untrusted"` and takes secrets only as references; the agent panel lists MCP servers and skills and each turn's use of them.

[[cmd/umb/mcp.go#cmdMcp]] has no trust flag, and [[internal/api/mcp.go#handleMcpAdd]] refuses any other `trust` from a `cli` connection, so neither a copied script nor raw JSON-RPC can add a trusted server. [[cmd/umb/mcp.go#takeEnv]] removes every `--env` before Go's flag parser runs, because that parser quotes a refused value in its error and the value may be the secret; only `keyring:` and `env:` references pass. The `cli` client kind may call `mcp.server.*` ([[internal/api/mcp.go#mcpMethods]]).

The panel never patches its lists from a notification: `mcp.server_state` and `skill.changed` make it list again ([[internal/tui/commands.go#Model#loadServersCmd]]), relying on persist-before-notify, and opening the panel lists too because removing a server publishes nothing. [[internal/tui/agent.go#Model#extensionLines]] draws the two lists, each made one line per item and capped at two rows by [[internal/tui/agent.go#wrapItems]] so remote text cannot break the layout. A turn's `mcp_*` calls that reached the server and its successful `skill_load` names are summed into one row at its end.

## Discovery stores one model per id

[[internal/llmgw/catalog.go#Catalog#Refresh]] keeps the first entry when a provider lists one id twice, because a request names the model only by that id.

Ollama 0.40 lists a model pulled for two backends twice under one name, with different digests, and Ollama picks the copy when called. The `models` table keys on the id, so passing both to the store failed the whole replace and left the provider with no model at all. The check is in the catalog, since any provider's list can repeat a name, and [[internal/llmgw/adapters/ollama/ollama.go#Provider#Models]] keeps the same first copy for the window that caps `num_ctx`, so the two never describe different copies (delta `2026-10-duplicate-models`).

## A cancelled command's shell is settled before the next one

The thread's shell can keep an interrupt meant for a cancelled command and spend it on the next line, so the next agent command is typed after an empty line.

[[internal/sessions/agent.go#Service#stopAgent]] interrupts the shell again while a cancelled command stays silent, and one interrupt can land after the command ended but before the daemon read the block's close. bash and zsh keep it pending and drop the first byte of the next line they parse: `echo still-here` ran as `cho still-here`. [[internal/sessions/agent.go#Service#signalRun]] marks the session interrupted, and [[internal/sessions/agent.go#Service#RunForThread]] then types an empty line before the command: the interrupt eats that byte, and the empty line runs nothing (delta `2026-10-agent-pty-settle`).

The empty line opens no block only because [[shell/bash/umbral.bash]] disarms its DEBUG trap in `__umbral_save_status`, the first hook of `PROMPT_COMMAND`. Armed through the prompt, the trap took a user's own hook (Starship, direnv) for a command, and its phantom block took the agent's claim. [[internal/sessions/service.go#Config]]'s `AgentInterruptEvery` lets a test interrupt every millisecond, which turns the race from rare into routine.

## Shutdown records every exit before the store closes

[[internal/sessions/service.go#Service#Shutdown]] hangs up every shell and waits, up to [[internal/sessions/service.go#shutdownGrace]] in all, for each session's exit to be persisted.

It used to return at once, so `cmd/umbrald` closed the store while the sessions were still exiting and each logged `persist the session exit: sql: database is closed`. A shell that ignores the hang-up is left to recovery on the next start rather than holding the daemon's exit.

## A script drives a thread through umb

`umb thread` gives a script one command per step — create, send with a wait, wait, read, cancel — so it drives a thread without polling and without posing as the TUI (REQ-CLI-009).

[[cmd/umb/thread.go#cmdThread]] makes one call per command and prints only the value the next step needs: the thread id, the turn id with its final state, the state a wait answered. A `TIMEOUT` exits 1 naming the state last observed. The `cli` client kind may call `thread.wait` ([[internal/api/waits.go#waitMethods]]) and `thread.get` ([[internal/api/threads.go#threadMethods]]), which only read; `thread.list`, `update`, `focus` and `approval.*` stay interactive, so a script cannot switch a thread to `auto-edit` and hands an approval to a person (delta `2026-10-cli-thread-wait`).

## A worktree lands without a checkout

[[internal/worktrees/worktrees.go#Service]] gives a thread its own copy of a repository outside it. A merge squashes that copy into the branch it came from, in the user's clean working copy, and never checks out another branch (delta `2026-10-worktree-engine`).

[[internal/worktrees/adapters/gitcli/gitcli.go#CLI#Squash]] does this in four steps:
- it writes the worktree's whole working tree as a tree through a private index;
- it wraps that tree in an unsigned commit on the base;
- it runs `cherry-pick --no-commit`, then `commit`;
- after a conflict or a failed commit, it rolls back with `reset --hard`.

The rollback is exact because [[internal/worktrees/worktrees.go#Service#Merge]] refuses a main copy that is dirty, on another branch or detached before anything runs. git 2.34 has no `merge-tree --write-tree`, which is why the squash goes through a working copy.

Before the cherry-pick, a path the squash adds where the main copy already has a file is refused as a conflict. git would overwrite an ignored file such as `.env`, and the rollback would then delete it. A commit that moved `HEAD` before its timeout has landed and is not rolled back.

Showing a worktree runs git with fsmonitor, filters, external diff and textconv off. Landing it runs the user's hooks and signing, in a session of its own with no terminal, and the process group is killed at `[worktrees] commit_timeout`.

The worktree's own `.git` file is never read, because the agent can rewrite it. [[internal/worktrees/adapters/gitcli/gitcli.go#CLI#pinned]] pins `GIT_DIR` and `GIT_WORK_TREE` from the main repository's record of the worktree, and removal deletes the recorded directory, then prunes. The filter drivers turned off while showing are listed through the same pinned directory.

A diff answers with the revision it was read from. A merge that passes it is refused `changed` when the worktree no longer holds that content. The check runs inside the squash against the snapshot being committed, so work slipped in after the review never lands.

[[internal/worktrees/domain/domain.go#ParseUnifiedDiff]] gives each hunk an id from its path and lines, not its position, so a hunk keeps its id while an earlier one grows.

[[internal/worktrees/worktrees.go#Service#CheckAll]] marks a worktree missing when its directory, branch or repository is gone, and never recreates it.

## Agent detection labels a pane and never decides its state

[[internal/integrations/detect.go#Detector]] names the third-party agent running in a pane: its root process or anything under it. It is a label beside the pane: never an input of DD-012's state precedence (delta `2026-10-agent-detection`).

The built-in manifests in [[internal/integrations/domain/agents.go#Builtins]] are overlaid by name with the local ones that [[internal/integrations/adapters/manifests/manifests.go#Load]] reads once at start from `$XDG_CONFIG_HOME/umbral/agents/`. Unknown keys make a file invalid, and a skip reason names the field, key or line without quoting a value. [[internal/integrations/adapters/proctree/proctree.go#Tree#Snapshot]] reads the process table once per scan, and a pane's subtree is its root process — the agent itself when the pane was launched with it — then what runs under it, breadth first. [[internal/integrations/domain/agents.go#Detect]] tries them in that order against the manifests in name order, so the answer is deterministic.

Labels — name and declared state source, nothing of the process — live in memory, keyed by session, in [[internal/integrations/detect.go#AgentLabels]]; the workspace tree reads them into `Pane.detected_agent`, and a change is announced as `pane.updated`, never `pane.state_changed`. [[internal/integrations/detect.go#Detector#Explain]] backs `agent.explain` and `umb agent explain`, naming the matching pid and executable basename but never the argv, which can carry a secret.
