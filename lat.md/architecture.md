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
