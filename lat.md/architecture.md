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

A second implementation could drift from the real policy and explain decisions the daemon never makes. For the same reason the explainer refuses a risk the registered tool does not declare, shapes a file target with the built-ins' own [[internal/tools/adapters/builtin/builtin.go#PolicyPath]], and shares one [[cmd/umbrald/policy.go#isRepo]] with the runtime. A thread's directory, write root and effective mode come from [[internal/agents/runtime.go#TurnPlace]], so a thread in its worktree is explained with the worktree's, and a thread in `full-auto` under `full-auto`, as its turn runs.

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

## The destructive floor reads a line as bash does

A command is matched against the destructive patterns after bash's own parser has read it, so quotes and separators cannot hide a program; what the line does not show asks (delta `2026-10-shell-parser`).

[[internal/security/domain/policy_destructive.go#MatchDestructive]] walks every simple command the `mvdan.cc/sh/v3/syntax` parser finds, inside substitutions too, drops wrappers and launchers such as `setsid` and `systemd-run` (reading a flag their table lacks both ways), and reads a script handed to a shell, `eval`, `watch`, `ssh`, `trap`, `su -c` or a here-document as a line of its own; an interpreter's inline program, or the one it reads from a here-document, is scanned with the old separator split, and flag values that run a command — ssh's `-o ProxyCommand`, strace's `-o '|cmd'` — are read as scripts, short-flag clusters letter by letter via [[internal/security/domain/policy_destructive.go#normaliser#wrapperFlag]]. With no pattern matching, [[internal/security/domain/policy_destructive.go#UnknownDynamicWord]], [[internal/security/domain/policy_destructive.go#UnknownStdinScript]] or [[internal/security/domain/policy_destructive.go#UnknownUnparseable]] still asks and names the reason in the trace; arithmetic, history expansion, prompt variables and `tmux`/`screen` count as dynamic, output redirections count on any statement, and [[internal/security/domain/ruleset.go#ParseRules]] keeps those three names out of rule files. It is the one domain package allowed a vendor, which `.go-arch-lint.yml` grants to `security-domain` alone. Both `run_command` and an ACP `execute` permission are decided through it.

## Waits are inventoried and stalls only inform

Every wait is registered before it subscribes, so a leaking script hits the 32-per-connection limit as an error, never a dropped connection; a stall is announced, never acted on (DD-017).

[[internal/waits/waits.go#Service]] admits a wait under its connection's count and a `wt_` id from [[internal/store/ids.go#PrefixWait]] before it costs a bus subscription, and [[internal/waits/waits.go#Service#Cancel]] ends only the waiter: the turn, command or pane it watched goes on.

Stall detection sits in the agent runtime because it already sees every event of its turns. [[internal/agents/stall.go#Runtime#observe]] runs inside `publish`, before the lossy bus, so backpressure cannot erase activity. [[internal/agents/stall.go#Runtime#watchStalls]] marks a turn idle past its window once and never cancels it, because a local model may take minutes; a turn awaiting approval is waiting for the user and never stalls, whatever its pane writes meanwhile, and neither does a turn held for a slot or waiting for its provider (see the parallel-turns section below). The window is the local one whenever [[internal/llmgw/router.go#Router#Local]] finds a local candidate.

`sessions` and `agents` cannot import each other, so [[cmd/umbrald/stall.go#followThreadOutput]] turns the output of a thread's own session into [[internal/agents/stall.go#Runtime#NoteOutput]].

External reporters are limited in [[internal/integrations/rate.go#Service#overRate]]: 10 kept reports per source per sliding second, state and metadata together, checked after validation and the pane's resolution so an unknown or agent-owned pane still answers its error. The excess answers like a kept report so a hook never fails for being chatty, and only written reports use up the window ([[internal/integrations/rate.go#Service#kept]]), so a flooding source is heard again as soon as it slows down.

## Skills are text, installed exactly as inspected

A skill is a directory of text for Umbral's own agent; the store copies it and executes nothing, and installs only the bundle whose digest the user saw (delta `2026-10-skill-store`).

[[internal/context/domain/skill.go#BundleBuilder]] enforces the per-file, per-bundle and entry limits as each entry is read, so a tarball bomb stops at the first byte over, and [[internal/context/domain/skill.go#IsRemoteSource]] refuses a URL or remote before any path is resolved: network material must be signed (Art. 5), and nothing signs skills yet.

[[internal/context/domain/skill.go#Digest]] covers file paths, sizes and contents only, so a directory and its tarball give the same digest and `skill.install` can refuse a bundle that changed after `skill.inspect`.

[[internal/context/adapters/skills/skills.go#Service]] writes through a staging directory renamed into place inside the row's transaction, with modes forced to `0644`/`0755`; [[internal/context/adapters/skills/skills.go#Service#Recover]] runs at start, from [[cmd/umbrald/skills.go#newSkills]], and removes every directory no row records — names starting with `.` cannot be skills, so staging and set-aside copies are covered by that one rule.

Because that sweep deletes, the store first claims its directory with a `.umbral-skills` marker and refuses a non-empty one without it. A replace interrupted between its renames is settled by digest: the copy at the skill's name stays only if it matches the row, else the set-aside copy comes back.

The user reaches the store through `umb skill` in [[cmd/umb/skill.go#cmdSkill]]. Install inspects, shows the manifest and installs exactly that digest, only after a `y` on a terminal or `--yes` ([[cmd/umb/skill.go#confirmInstall]]). [[cmd/umb/skill.go#sourcePath]] leaves a remote source unresolved so the daemon's refusal sees it as typed, and [[cmd/umb/skill.go#printable]] escapes every control rune of the bundle's own text, so the manifest a user confirms cannot hide a file.

The agent sees skills through [[internal/context/adapters/skills/skills.go#Service#Catalog]], read for every turn so a change reaches the next one: names and the start of each description, never a body (delta `2026-10-skill-catalog`). [[internal/context/domain/skill.go#Catalog]] folds each description to one line, because a bundle's text could otherwise open a fake section of the system prompt. The prompt then lists the entries inside one `<skills>` block, after a preamble naming them third-party text, not instructions; [[internal/context/domain/prompt.go#escapeEntry]] writes their `&`, `<` and `>` as entities, so no description can close the block. The agent reads a body only through [[internal/tools/adapters/builtin/skill.go#skillLoad]], whose every result taints the turn, so a skill's instructions cannot run a command without the user's approval. [[internal/context/adapters/skills/skills.go#Service#ReadSkill]] reads the stored copy once, checks it against the installed digest and serves the file from that same read, so a copy edited after install is refused rather than served. `umb skill install|enable|remove`, `umb mcp add|remove` and `umb rules` key changes, `rollback` and `reset` are destructive patterns in [[internal/security/domain/policy_destructive.go#DefaultDestructivePatterns]], so the agent cannot install what instructs it.

## MCP servers from the CLI are untrusted, and the panel shows what the agent reaches

`umb mcp add` always sends `trust: "untrusted"` and takes secrets only as references; the agent panel lists MCP servers and skills and each turn's use of them.

[[cmd/umb/mcp.go#cmdMcp]] has no trust flag, and [[internal/api/mcp.go#handleMcpAdd]] refuses any other `trust` from a `cli` connection, so neither a copied script nor raw JSON-RPC can add a trusted server. [[cmd/umb/mcp.go#takeEnv]] removes every `--env` before Go's flag parser runs, because that parser quotes a refused value in its error and the value may be the secret; only `keyring:` and `env:` references pass. The `cli` client kind may call `mcp.server.*` ([[internal/api/mcp.go#mcpMethods]]).

The panel never patches its lists from a notification: `mcp.server_state` and `skill.changed` make it list again ([[internal/tui/commands.go#Model#loadServersCmd]]), relying on persist-before-notify, and opening the panel lists too because removing a server publishes nothing. [[internal/tui/agent.go#Model#extensionLines]] draws the two lists, each made one line per item and capped at two rows by [[internal/tui/agent.go#wrapItems]] so remote text cannot break the layout. A turn's `mcp_*` calls that reached the server and its successful `skill_load` names are summed into one row at its end.

## The embedded provider runs llama.cpp in process

A `yzma` provider serves a GGUF model from inside the daemon, with no server and no socket (REQ-LLM-009, delta `2026-10-yzma-provider`).

[[internal/llmgw/adapters/yzma/yzma.go#Provider]] lists the `<name>.gguf` files of `models_dir` whose first bytes are `GGUF`, never opening a name with a separator or a leading dot, and loads llama.cpp's libraries from `lib_path` on its first call through [[internal/llmgw/adapters/yzma/yzma.go#libLoader]], once per process, since purego cannot unload them. The models are local and text only: tools, a schema, reasoning or vision are `ErrUnsupported`, so the router moves on. One model is resident per provider; another model's call frees it first, `keep_alive` frees it when idle, and [[internal/llmgw/catalog.go#Catalog#Configure]] closes a provider a reload replaces. [[internal/llmgw/adapters/yzma/llama.go#llamaModel#neutralize]] breaks any control token's text in a message, so only the template frames turns; [[internal/llmgw/adapters/yzma/llama.go#llamaModel#generate]] samples from an emptied cache, and the adapter streams only whole characters and refuses a prompt that leaves no room in the window before decoding.

## Discovery stores one model per id

[[internal/llmgw/catalog.go#Catalog#Refresh]] keeps the first entry when a provider lists one id twice, because a request names the model only by that id.

Ollama 0.40 lists a model pulled for two backends twice under one name, with different digests, and Ollama picks the copy when called. The `models` table keys on the id, so passing both to the store failed the whole replace and left the provider with no model at all. The check is in the catalog, since any provider's list can repeat a name, and [[internal/llmgw/adapters/ollama/ollama.go#Provider#Models]] keeps the same first copy for the window that caps `num_ctx`, so the two never describe different copies (delta `2026-10-duplicate-models`).

## A cancelled command's shell is settled before the next one

The thread's shell can keep an interrupt meant for a cancelled command and spend it on the next line, so the next agent command is typed after an empty line.

[[internal/sessions/agent.go#Service#stopAgent]] interrupts the shell again while a cancelled command stays silent, and one interrupt can land after the command ended but before the daemon read the block's close. bash and zsh keep it pending and drop the first byte of the next line they parse: `echo still-here` ran as `cho still-here`. [[internal/sessions/agent.go#Service#signalRun]] marks the session interrupted, and [[internal/sessions/agent.go#Service#runAgent]], which types both a thread's `run_command` and an ACP terminal's line, then types an empty line before the command: the interrupt eats that byte, and the empty line runs nothing (delta `2026-10-agent-pty-settle`).

The empty line opens no block only because [[shell/bash/umbral.bash]] disarms its DEBUG trap in `__umbral_save_status`, the first hook of `PROMPT_COMMAND`. Armed through the prompt, the trap took a user's own hook (Starship, direnv) for a command, and its phantom block took the agent's claim. [[internal/sessions/service.go#Config]]'s `AgentInterruptEvery` lets a test interrupt every millisecond, which turns the race from rare into routine.

## The agent drives its terminal key by key

`terminal_interact` sends keys to the thread's own PTY, the one `run_command` uses, and is decided on the line the shell would run or on a program's input as text (delta `2026-10-terminal-interact`).

[[internal/tools/adapters/builtin/terminal.go#terminalInteract#Action]] makes `send_keys` an `Exec` and the reads `ReadOnly`, which the registry allows only for a tool whose `Spec.ReadOnlyCalls` is set. At the prompt [[internal/sessions/domain/interact.go#PlanKeys]] accepts text, `backspace` and a final `enter` or `ctrl+c` only and assembles the line across calls, so an `enter` is decided on the whole line exactly as `run_command`'s. With a program in the foreground the keys are rendered as text and added to the program's line since its last enter, so a line split across calls is decided whole; [[internal/security/domain/program_input.go#matchProgramInput]] matches each line against the destructive patterns as written and through the shell parser, since a program that leaves its input unread hands it to the shell, and a line with shell expansion syntax is decided by the parser alone; a stdin script asks, and an unparseable line only when [[internal/security/domain/program_input.go#incompleteLine]] says the shell would join it to the next. [[internal/security/domain/program_input.go#programKey]] reads `space` as a space, `ctrl+m` and `ctrl+j` as line ends and `ctrl+h` as a backspace, a `ctrl+c` or `ctrl+d` between text and more text on one line asks, and the lines are matched joined too, as a shell would read them; any other key but `ctrl+c` and `ctrl+d` asks as [[internal/security/domain/program_input.go#UnknownProgramKey]] on the destructive floor. `DROP TABLE` and `x=rm; $x -rf ~` ask, `SELECT *` does not, and full-auto never runs program input unasked.

Reads never start or replace a shell: [[internal/sessions/interact.go#Service#readPTY]] answers `no_terminal` instead. [[internal/tools/adapters/builtin/terminal.go#terminalInteract#Run]] recomputes the target and refuses one that moved or was decided while the user held the terminal, and [[internal/sessions/interact.go#Service#SendKeys]] checks the decided state again under the session's input lock before writing. Named keys go through the emulator's key encoder, so `up` reaches `vim` as `ESC O A`. A call at the prompt that enters nothing returns once [[internal/sessions/interact.go#Service#awaitAnswer]] sees the shell's output settle, after [[internal/sessions/interact.go#Service#writeKeys]] has released the input lock: a `ctrl+c` sent while the editor had not read the text before it flushed part of the line, and CI ran `cho` for `echo pending`. [[internal/sessions/agent.go#Service#claimPTY]] refuses `run_command` while a program the agent started runs or its typed text sits on the prompt.

[[internal/sessions/interact.go#Service#TakeOver]] moves the input to the user under the same lock, after which every call answers `taken_over` and the PTY is never replaced; [[internal/sessions/interact.go#Service#HandBack]] returns it and types nothing: the agent's next write at the prompt starts with `ctrl+e ctrl+u`, which drops what the user left on the line, because a `ctrl+c` lost the race with bash drawing its prompt. [[internal/sessions/interact.go#Service#Release]] hands back what a closing connection held. The TUI binds `ctrl+]` in [[internal/tui/takeover.go#Model#takeOverKey]]: the thread's terminal replaces the agent panel and takes every key but `ctrl+]` and `ctrl+q`.

## A thread's zsh keeps a last line without a newline

zsh's `PROMPT_SP` writes a mark, spaces and a carriage return before each prompt, which erased a last line printed without a newline from what `run_command` returned.

[[internal/sessions/lifecycle.go#Service#umbralEnv]] sets `UMBRAL_AGENT_SESSION=1` only in a session a thread owns, and the name is one a pane never inherits. [[shell/zsh/.zshrc]] reads it after the user's rc, turns `PROMPT_SP` off, and turns it off again in `__umbral_precmd` and `__umbral_preexec`: zsh writes the mark before precmd runs, and preexec runs after every precmd hook, so even a framework hook appended late that sets it every prompt cannot bring the mark back. A user's own zsh pane keeps the option (delta `2026-10-zsh-prompt-sp`).

## Shutdown records every exit before the store closes

[[internal/sessions/service.go#Service#Shutdown]] hangs up every shell and waits, up to [[internal/sessions/service.go#shutdownGrace]] in all, for each session's exit to be persisted.

It used to return at once, so `cmd/umbrald` closed the store while the sessions were still exiting and each logged `persist the session exit: sql: database is closed`. A shell that ignores the hang-up is left to recovery on the next start rather than holding the daemon's exit.

## The agent panel lists the focused workspace's threads

The TUI's panel shows every thread of the daemon's focused workspace and switches between them with up and down; the selected thread is the one viewed, shown and sent to (REQ-ORC-003).

[[internal/tui/adapters/daemon/daemon.go#Daemon#Threads]] reads the focused workspace and the threads from one `session.snapshot`, and [[internal/tui/agent.go#Model#shownThreads]] keeps that workspace's, or with none focused those of none, plus the selected one. [[internal/tui/agent.go#Model#switchTo]] keeps the current [[internal/tui/agent.go#threadView]] and restores the other's, so every thread's transcript survives a switch; [[internal/tui/agent.go#Model#applyAgentEvent]] folds each event into its thread's view and lists again after a turn's end, an attention or focus change, and a queued thread's first event ([[internal/tui/agent_test.go#TestAgentPanelListsEveryThread_REQ_ORC_003]]). [[internal/tui/agent.go#Model#threadsLoaded]] drops a listing older than the applied one by its snapshot `seq`, marks a thread running when the listing does, whoever started its turn, and releases the views of threads no longer listed.

## A client that stops reading holds up no other

The dispatcher only queues control notifications; each connection's own goroutine writes them, and a connection 8 MiB behind is closed rather than trimmed.

[[internal/api/notify.go#Server#broadcast]] hands each notification to [[internal/api/outbox.go#conn#enqueue]], which frames it into the connection's [[internal/api/outbox.go#outbox]]; [[internal/api/outbox.go#conn#writeOutbox]] writes it in dispatch order. Past [[internal/api/outbox.go#ClientNotificationQueueBytes]] the connection is closed, because a dropped `pane.closed` would leave the client's cache silently wrong, and the client reconnects with a snapshot. There is no write deadline: a suspended TUI harms only itself ([[internal/api/slowclient_test.go#TestASlowClientHoldsUpNoOtherClient_REQ_API_006]]).

## block.get shortens its output to fit the frame

A `block.get` over the connection's frame limit answers with most of its output and says how much it left out, instead of `RESULT_TOO_LARGE` (REQ-API-005).

[[internal/api/blocks.go#fitBlockResult]] cuts the one output field asked for: raw on a 3-byte boundary before base64, plain on a rune boundary measured as JSON. [[internal/api/blocks.go#plainPrefix]] adds up each rune's [[internal/api/blocks.go#escapedWidth]] in one pass and has the encoder confirm the cut, falling back to a bisection over the encoder if they disagree. A bisection alone encoded a megabyte about twenty times per response, which outlasted a client's read on a loaded runner ([[internal/api/blocks_fit_test.go#TestAPlainCutMatchesTheEncoder_REQ_API_005]], [[internal/api/blocks_fit_test.go#TestAPlainCutEncodesAFewTimes_REQ_API_005]]).

## umb ai never drops an answer's deltas

`umb ai` prints every delta of an answer, so its stream queues notifications without bound instead of overflowing when its reader falls behind.

[[internal/client/stream.go#NewUnboundedStream]]'s reader never stops reading the socket: notifications go to a queue that [[internal/client/stream.go#Stream#forward]] delivers in order, so a reply is never held behind unread notifications and the daemon's writer is never held by this client. The TUI keeps [[internal/client/stream.go#NewStream]], which ends with `ErrStreamOverflow` and recovers with a snapshot. [[cmd/umb/ai.go#relay]] keeps the thread's own events for a slow stdout ([[internal/client/stream_test.go#TestAnUnboundedStreamKeepsEveryNotification_REQ_CLI_001]]).

## A script drives a thread through umb

`umb thread` gives a script one command per step — create, send with a wait, wait, read, cancel — so it drives a thread without polling and without posing as the TUI (REQ-CLI-009).

[[cmd/umb/thread.go#cmdThread]] makes one call per command and prints only the value the next step needs: the thread id, the turn id with its final state, the state a wait answered. A `TIMEOUT` exits 1 naming the state last observed. The `cli` client kind may call `thread.wait` ([[internal/api/waits.go#waitMethods]]) and `thread.get` ([[internal/api/threads.go#threadMethods]]), which only read; `thread.list`, `update`, `focus` and `approval.*` stay interactive, so a script cannot switch a thread to `auto-edit` and hands an approval to a person (delta `2026-10-cli-thread-wait`).

`umb thread create --worktree` gives the thread its worktree at creation — when git refuses it the thread is still made, and stderr names the refusal — and the thread joins the pane's workspace, `UMBRAL_WORKSPACE_ID`, unless `--workspace` names another.

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

## A thread runs in its active worktree

A turn reads its thread's active worktree as it starts and runs there; the thread's own `cwd` never changes, so a merge or discard puts the next turn back home (delta `2026-10-worktree-methods`).

[[internal/agents/runtime.go#TurnPlace]] asks [[internal/agents/ports/ports.go#Worktrees]] — which `cmd/umbrald` adapts to [[internal/worktrees/worktrees.go#Service#ActiveFor]], the missing-check included — and makes the worktree the write root and [[internal/agents/runtime.go#worktreeCwd]] the cwd: the worktree's directory matching the thread's while it resolves inside the worktree, or its top. `policy.explain` calls the same function through `cmd/umbrald`'s `explainSource`, so an explanation and a turn decide with the same directories. The thread's PTY follows: [[internal/sessions/agent.go#Service#threadSession]] closes a shell started for another cwd and starts one in the new one.

A worktree never changes under a running turn. [[internal/agents/runtime.go#Runtime#WhileIdle]] holds the thread's send lock, which a send keeps until its turn is written as running, and refuses with `ErrTurnRunning` while the stored thread is `running` or `awaiting_approval` — the store, not the turn map, because a turn's end is written before `thread.turn_finished` and its goroutine lets go after. `worktree.create`, `merge`, `discard` and the discards of [[internal/worktrees/worktrees.go#Service#CloseGroup]] all go through it, the last nesting every thread's guard in id order around all its discards; the API answers `CONFLICT` with `data.reason = running`.

[[internal/agents/runtime.go#Runtime#Create]] with a worktree writes the thread, then the worktree. A refusal the user can act on arrives wrapped in [[internal/agents/ports/ports.go#ErrWorktreeRefused]]: the thread is kept without isolation and `thread.create` answers it with `worktree_refused` (REQ-WT-008). Any other failure deletes the thread.

`worktree.*` is served by [[internal/api/worktrees.go]] over the engine's inbound port [[internal/worktrees/ports/ports.go#Worktrees]]; [[internal/api/worktrees.go#worktreeErrorData]] carries a refusal's `reason`, `paths` and `output`. `close_group` reports every worktree of the group with `changed` or `clean` and discards only the active ones it names, after every check has passed. `umb worktree merge` prints the diff, asks, and merges with the diff's revision, waiting for the daemon rather than `umb`'s 10-second call bound; `umb worktree merge|discard` is on the destructive list. A merge whose commit landed answers it even when the worktree's removal fails ([[internal/worktrees/domain/domain.go#MergeResult]]'s `Leftover`, logged).

## Full-auto decides only inside an active worktree

`full-auto` is a thread's flag beside its own mode: a turn decides under it only while its worktree is active, so writes and commands starting inside the worktree run unasked and everything else asks as before (delta `2026-10-full-auto`).

[[internal/agents/runtime.go#TurnPlace]] returns a turn's cwd, write root and effective mode together, read once as the turn starts: `full-auto` when [[internal/agents/domain/thread.go#Thread]]'s `FullAuto` is set and the worktree is active, the thread's own mode otherwise. The policy engine's mode step ([[internal/security/domain/policy.go#Policy#Decide]]) then allows `WriteFS` inside the write root and `Exec` whose cwd is inside it ([[internal/security/domain/policy.go#cwdWithinWriteRoot]]); the destructive floor, deny rules and taint come first, and `Network` falls to the mode default. That cwd is the one the turn hands the tool, not the shell's current directory, which the daemon does not track.

[[internal/agents/runtime.go#Runtime#Update]] asks the engine for the thread's active worktree before it lets the thread in, and the thread store's write requires the worktree row to be `active` in the same statement; `thread.create` refuses `full-auto`. Migration `0008_full_auto`'s triggers clear the flag in the write that moves the worktree out of `active` or takes it from its thread. The wire reports `full-auto` through [[internal/agents/domain/thread.go#Thread#WireMode]].

An agent's terminal cannot land its own work. [[internal/api/conn.go#conn#fromAgent]] asks `Config.AgentPeer` about the kernel's peer pid of the connection, and every method marked `userOnly` — `worktree.merge`, `discard`, `close_group`, `approval.respond`, `thread.update`, the skill, MCP and rule changes, `session.input`, and every method that starts a process (`session.create`, `pane.split`, `tab.create`, `workspace.create`, `layout.apply`) — answers `PERMISSION_DENIED` to a client inside one, whatever kind it claims. The verdict taken when the connection starts holds for it, and while an agent's terminal is open a peer that cannot be read is taken for one. [[cmd/umbrald/agentpeer.go#agentPeer]] composes the check from [[internal/sessions/service.go#Service#AgentShellPIDs]], the hosted ACP agents' [[internal/acp/adapters/sdk/host.go#Host#AgentPIDs]], [[internal/integrations/adapters/proctree/proctree.go#Ancestors]] and the POSIX session id; a process that calls `setsid` and forks away from its parents escapes it. A hosted agent leads its own session and counts from its launch, because its isolated `HOME` sits beside the socket and its token (delta `2026-10-hosted-agent-peer`).

## Turns run in parallel up to a limit, then queue in order

Turns of different threads run at once, each in its own goroutine; one over `[agents] max_concurrent_turns` is accepted, written `queued`, and started in arrival order when a turn ends (delta `2026-10-parallel-turns`).

[[internal/slots/slots.go#Queue]] is a counting semaphore that grants the oldest waiter first. It is a leaf of the standard library only, so the runtime and the router share it without `llmgw` reaching `agents`.

[[internal/agents/runtime.go#Runtime#Send]] takes the turn's [[internal/slots/slots.go#Ticket]] before the store's transaction, so the order of places is the order sends arrived and `thread.get` says `queued` the moment the send answers. The thread is `running` and `working` meanwhile, so every guard that reads a running turn from the store — a second send, a mode change, [[internal/agents/runtime.go#Runtime#WhileIdle]] — treats a held turn as running. [[internal/agents/turn.go#turnRun#takeSlot]] waits before the turn reads anything, clears `queued` once granted and restarts the stall clock; a cancel meanwhile ends the turn `cancelled` with no model call, and the place is released only after the turn's end is written. A held turn is skipped by [[internal/agents/stall.go#Runtime#newStalls]].

The router queues model calls per provider the same way: [[internal/llmgw/router.go#Router#attempt]] waits for its place in [[internal/llmgw/router.go#Router#queue]] before the first-token timer starts, so time behind another thread's call is never a timeout nor part of `first_token_ms`. [[internal/llmgw/router.go#Router#SetParallel]] applies each provider's `max_parallel`; absent, a local provider takes one call at a time and a remote one has no limit, recomputed from the queue's locality on every reload. The place belongs to the pump, released once the provider's stream returns, so a request abandoned at its first-token timeout is closed before the next is sent. A call that must wait yields the router's own `queued` event, then `sent` once it holds its place; [[internal/agents/turn.go#turnRun#stream]], the one wrapper of every model call of a turn, compaction included, hides both and calls [[internal/agents/stall.go#Runtime#waitForProvider]] so the turn is not marked stalled between them.

## A hosted ACP agent runs its thread's turns

A thread created with `agent` is hosted by a third-party agent that speaks ACP: the runtime keeps the thread, its store, slot, attention and cancel, and swaps the model loop for one `session/prompt` (delta `2026-10-acp-client`).

[[internal/agents/runtime.go#Runtime#Create]] asks [[internal/agents/ports/ports.go#ExternalAgents]] to check the entry before anything is written, then writes the thread `unobserved`, makes its worktree, and opens the agent with [[internal/agents/runtime.go#Runtime#openAgent]] where [[internal/agents/runtime.go#TurnPlace]] says its turns run; a hosted thread without a worktree has its own directory as its write root. An agent that does not start leaves the thread with `AgentUnavailable`, which `thread.create` answers as `agent_unavailable`. [[internal/agents/turn.go#turnRun#run]] hands a hosted turn to [[internal/agents/external.go#turnRun#runExternal]], whose [[internal/agents/external.go#externalSink]] persists each chunk and tool call before publishing it, the calls named `acp:<kind>` with `via = acp` and `unmediated` until a permission decides them.

[[internal/acp/adapters/sdk/host.go#Host]] is the client on acp-go-sdk, which `cmd/umbrald` adapts as [[cmd/umbrald/acp.go#runtimeAgents]]. [[internal/acp/adapters/sdk/host.go#Host#launch]] checks again at every launch, writes the manifest's [[internal/acp/domain/manifest.go#Manifest#Generate]] output into a fresh `acp/<thread>/` home and starts the entry's command leading its own session — its pid counts as an agent's for the socket from launch, and at exit [[internal/acp/adapters/sdk/reap_linux.go#waitExit]] sees it before it is reaped, so [[internal/acp/adapters/sdk/reap_linux.go#endSession]] kills everything left in its session — through pidfds, with a 2 s deadline past which the pid stays listed — and its pid is forgotten while the pid cannot yet be reused (macOS reaps first, so there the pid stays listed until the agent is closed) — with [[internal/acp/domain/env.go#Environment]]: `PATH`, `LANG`, the isolation, the configuration's variable and the resolved `env_refs`. [[internal/acp/domain/manifest.go#Builtin]] has OpenCode, every permission `ask` with the wildcard first, and [[internal/acp/adapters/sdk/host.go#findProjectConfig]] refuses a repository's own configuration up to its top. The isolation keeps the user's configuration out, not the network: measured with OpenCode 1.18.33, an empty isolated cache makes the agent fetch its provider package from npm and a model catalogue at start, its own traffic, which the thread's `unobserved` discloses (delta `2026-10-usf2-live`). An `env_refs` key the manifest reserves ([[internal/acp/domain/manifest.go#Manifest#ReservesEnv]], `OPENCODE_*`) stops the daemon, and the agent's stderr is `/dev/null`, not kept since it may print the credentials those refs carry, and not a pipe a leftover child could hold open to hide the exit; for the same reason [[internal/acp/adapters/sdk/prompt.go#replyError]] reports an agent's error answer by its JSON-RPC code only. The search for a repository's configuration stops only at a `.git` git would accept ([[internal/acp/adapters/sdk/host.go#isGitEntry]]) — a linked worktree's gitfile included, its objects found through `commondir` — as the agent's own lookup would.

[[internal/acp/adapters/sdk/prompt.go#Host#Prompt]] ends a turn `agent_error` and kills the process when it exits, when [[internal/acp/adapters/sdk/process.go#checkedReader]] meets a line that is not JSON-RPC 2.0 — the SDK alone logs and reads on — or after `[acp] silence_timeout` without a line, a clock held while a request of the agent's is unanswered or an approval of its thread is pending. A cancel first stops the commands of the agent's terminals with [[internal/acp/adapters/sdk/terminal.go#process#killTerminals]], keeping their sessions, so REQ-ACP-008's 500 ms does not wait on the agent — one still starting is killed as [[internal/acp/adapters/sdk/terminal.go#process#addTerminal]] registers it, until the next turn; it then sends `session/cancel` and kills the process after 5 s (delta `2026-10-acp-cancel-traces`). `[acp]` and `[[acp.agents]]` are split out of `config.toml` by [[internal/config/acp.go#splitACP]] and decoded strictly by [[internal/config/acp.go#parseACP]].

### The running turn mediates what the agent asks

A hosted agent's permissions, file requests and terminals are decided by the turn running when they arrive, through the runtime's sink; one arriving between turns is refused (delta `2026-10-acp-mediation`).

[[internal/agents/external.go#externalSink#Permission]] decides a permission per target — each location as `write_file` resolves it, an `execute`'s command and cwd as `run_command`, a `fetch`'s URL — with [[internal/agents/external.go#externalSink#decide]] keeping the strictest verdict and asking when nothing is named; a target the tools cannot resolve is refused and recorded `deny`. The reported call's row takes the decision, an ask enters the thread's approval queue, and [[internal/acp/adapters/sdk/client.go#client#RequestPermission]] answers `allow_once` or `reject_once`, never `allow_always`. An allowed permission's locations cover the turn's later writes to them.

[[internal/agents/external.go#externalSink#Request]] gives each `fs/*` and `terminal/create` a row of its own and decides it in [[internal/agents/external.go#externalSink#requestDecision]]: a read is `ReadOnly` and asks outside the write root, a write is `WriteFS` unless covered, a terminal is `run_command`'s decision on the line [[internal/acp/domain/request.go#CommandLine]] quotes and asks for a cwd outside the write root even when a rule allows the line. Only what was allowed is performed, on the decided target: files by [[internal/tools/adapters/builtin/files.go#ReadText]] and [[internal/tools/adapters/builtin/files.go#WriteText]], terminals by [[internal/sessions/terminal.go#Service#StartTerminal]], a thread-owned session in bash or zsh — never fish, whose quoting differs — that returns once the line is written, its command a `via = acp` block never taken for the thread's PTY. [[internal/acp/domain/request.go#CheckTerminal]] refuses a control character in a terminal's command, arguments or environment before anything is decided, since the shell's line editor acts on one even inside quotes; an agent's own commands are held to the same rule. The policy also decides [[internal/acp/domain/request.go#PolicyLine]], one token per word the shell sees, because the destructive normaliser reads no quotes and `env 'A=x y' rm -rf /` would otherwise hide `rm`; the strictest verdict stands. A terminal whose words may hold a script of their own asks whatever else allows it: [[internal/acp/domain/request.go#InterpretsScript]] reads no flag grammar and fails closed: any word naming a shell, busybox, PowerShell, `ssh`, `eval` or a runner of shell lines (`watch`, `su`, `script`, `flock`), whatever its flags or case, and `env` with any option outside a safe few. [[internal/acp/adapters/sdk/terminal.go#process#reserveTerminal]] enforces `[acp] max_terminals` before anything is asked, and an agent that ends releases its terminals.

### A hosted turn is traced and disclosed as unobserved

A hosted turn has the turn span of Umbral's own, with a child span per reported tool call and per mediated request, and every client says what Umbral cannot see of it (delta `2026-10-acp-cancel-traces`).

[[internal/agents/external.go#externalSink#traceCall]] opens `tool.acp:<kind>` on a call's first row and ends it on the first status past `in_progress`; [[internal/agents/external.go#externalSink#endSpans]] ends the calls still open when the turn returns. [[internal/agents/external.go#externalSink#Request]] wraps each `fs/*` and `terminal/create` in a `tool.acp:<method>` span from its arrival, ended with its outcome or as an error when refused before its row. `umb thread create --agent` writes on stderr that the agent's own network traffic and file reads are not recorded, and [[internal/tui/agent.go#Model#threadLines]] lists a hosted thread with its agent and `unobserved`, adding that notice while it is selected.

## Agent detection labels a pane and never decides its state

[[internal/integrations/detect.go#Detector]] names the third-party agent running in a pane: its root process or anything under it. It is a label beside the pane: never an input of DD-012's state precedence (delta `2026-10-agent-detection`).

The built-in manifests in [[internal/integrations/domain/agents.go#Builtins]] are overlaid by name with the local ones that [[internal/integrations/adapters/manifests/manifests.go#Load]] reads once at start from `$XDG_CONFIG_HOME/umbral/agents/`. Unknown keys make a file invalid, and a skip reason names the field, key or line without quoting a value. [[internal/integrations/adapters/proctree/proctree.go#Tree#Snapshot]] reads the process table once per scan, and a pane's subtree is its root process — the agent itself when the pane was launched with it — then what runs under it, breadth first. [[internal/integrations/domain/agents.go#Detect]] tries them in that order against the manifests in name order, so the answer is deterministic.

Labels — name and declared state source, nothing of the process — live in memory, keyed by session, in [[internal/integrations/detect.go#AgentLabels]]; the workspace tree reads them into `Pane.detected_agent`, and a change is announced as `pane.updated`, never `pane.state_changed`. [[internal/integrations/detect.go#Detector#Explain]] backs `agent.explain` and `umb agent explain`, naming the matching pid and executable basename but never the argv, which can carry a secret.

## The router filters, breaks and orders by policy

[[internal/llmgw/policy.go#Router#Route]] and every routed call share one plan, so `model.route` answers exactly the order a call of that shape would walk (delta `2026-10-policy-router`).

[[internal/llmgw/policy.go#Router#plan]] drops a candidate that is not in the catalog, is down, lacks a capability in [[internal/llmgw/policy.go#Needs]], or sits behind an open breaker, then orders the kept ones with [[internal/llmgw/policy.go#compareBy]]: `local-first`, `cost` (an unpriced remote model last) or `quality` (declared order). The dropped ones follow in declared order.

The [[internal/llmgw/policy.go#breakers]] count the failures a call falls back on and open after `breaker_failures` in a row for `breaker_cooldown`. After it, the next real call is the one probe: its first event closes the breaker at once and a failure reopens it. While a breaker is open only the probe counts, and each opening and closing is logged by [[internal/llmgw/router.go#Router#breakerMoved]]. A 4xx, an unsupported request or a cancel counts nothing.

## The .deb ships the branding kit as it is

[[packaging/deb/build.sh]] stages the three binaries, the eleven `hicolor` files and the `.desktop` entry copied from the kit, never regenerated (REQ-PKG-006), so the package carries what the `icons` job proved.

libghostty-vt is linked statically, so `Depends` names only the newest glibc symbol version the binaries reference. Only a `vMAJOR.MINOR.PATCH` tag is a release; an untagged build becomes `0.0.0+git.<hash>` and a prerelease `~rc`, so apt upgrades from either to the release. Modes and mtimes are fixed, so one commit builds one package. [[packaging/deb/postinst]] and [[packaging/deb/postrm]] refresh the icon cache and the desktop database only where the tools exist, so a minimal system installs without them.

[[packaging/deb/check.sh]] reads the package back with `dpkg -c` and compares each file with the kit; [[packaging/deb/check_selftest.sh]] proves it rejects four broken packages; and [[packaging/deb/smoke.sh]] installs it in a clean Ubuntu 26.04 container, lets `umb` autostart the installed daemon, and checks the icon cache before and after removal. `task deb` and `task deb:smoke` run them, as does the CI `deb` job.
