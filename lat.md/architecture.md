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

A hook raises one with `umb notify` ([[cmd/umb/notify.go#cmdNotify]]), which exits 0 even when the notification was not shown: `rate_limited` or `no_client` is an answer about the user, not a failure of the hook.

## Explaining a decision uses the turn's engine

[[internal/security/explain.go#Explainer#Explain]] answers `policy.explain` by calling the same `Decide` a turn calls, with the thread's mode, rules and write root (delta `2026-10-policy-explain`).

A second implementation could drift from the real policy and explain decisions the daemon never makes. For the same reason the explainer refuses a risk the registered tool does not declare, shapes a file target with the built-ins' own [[internal/tools/adapters/builtin/builtin.go#PolicyPath]], and shares one [[cmd/umbrald/policy.go#isRepo]] with the runtime.

## One rule set for every decision and redaction

[[cmd/umbrald/rules.go#loadRules]] builds the rules in force once at start: the built-in ones with the files of `rules/` beside `config.toml` on top (delta `2026-10-rule-overrides`).

Layers stack by name in [[internal/security/domain/ruleset.go#Layer]]: a same-named entry replaces the base's and a new one adds to it, so an override can replace a rule but never remove one. The base is the built-in set, or a verified bundle that replaces it whole. An invalid file is skipped whole with a warning naming it.

The agent runtime and `policy.explain` read the policy from the same [[internal/security/adapters/rules/active.go#Active]], and every redaction reads its redactor, so an explanation cannot describe a policy the daemon does not apply. [[internal/security/adapters/rules/active.go#Active#SetBase]] swaps that base and keeps the local layer on top.

The opt-in fetch in [[internal/security/adapters/rules/fetch.go#Fetcher]] writes every request, and every redirect hop, to `egress_log` before sending it, refuses a configured URL the redactor would change, and never applies what it fetched: until signatures can be verified, a bundle is logged and discarded.
