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
