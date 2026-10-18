# Delta — notifications: the foreground client, the order of the checks, and who may call

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-18` by the implementing agent under the Tech Lead's standing instruction for F1 ("si creas deltas, documentalos, apruebalos"); **pending the Tech Lead's review**. Folded into API 1.35, Tech Design 1.44 and the F1 tasks file |
| **Date** | 2026-10-18 |
| **Task** | T-F1-27 (REQ-NTF-001, REQ-NTF-002) |
| **Raised by** | API §5.35 gives the params and the result. It does not say: how the daemon reaches the foreground client, or which client that is; in what order `disabled`, `rate_limited` and `no_client` are checked; whether a request nobody receives counts against the source; what the `source` may hold; what an unknown `sound` does; how notifications are turned off; or how delegation decision 15 of `2026-10-integration-surface` words its notification |

## Evidence

- **REQ-NTF-001**: normalize, "deliver it to the foreground client", answer one of four reasons.
- **REQ-NTF-002**: more than 5 per `source` in 60 s → discard and `rate_limited`, without blocking other sources.
- **API §5.35**: `{title, body?, source, sound?}` → `{shown, reason}`. §6 has no notification that carries one to a client.
- **Tech §5.2**: `notify` is a sink and imports no other module.
- **Delta `2026-10-integration-surface`, decision 15**: an external `blocked` becomes a user notification through the same normalization and limit.

## Decisions

1. **A new notification, `notification.posted {title, body, source, sound}`** (API §6), carries the normalized notification to one client. It is never broadcast.
2. **The foreground client** is the authenticated `tui` or `desktop` connection that last sent a request, whatever the request and whether or not it succeeded. An activity counter orders them, not a clock, so two requests in the same millisecond are still ordered. A `cli` connection is never the foreground client; with no interactive connection the answer is `no_client`.
3. **Order of the checks:** validation (`VALIDATION_ERROR`, nothing counted), then `disabled`, then `rate_limited`, then `no_client`, then `shown`.
4. **A request that reaches the rate check counts** against its source whether or not a client receives it. The limit is on what a source asks for; otherwise a hook looping while no TUI is open would burst the moment one connects.
5. **The window slides**: at most 5 requests per source in any 60 s. A discarded request is never delivered later.
6. **`source` follows §5.31's rule** (1–80 characters of `[A-Za-z0-9:._-]`), so one hook uses one name for its reports and its notifications. The `umbral:` prefix is reserved for the daemon's own components, which call the sink directly.
7. **`sound`**: absent is `none`; anything but `none`, `done` and `request` is `VALIDATION_ERROR`. The daemon plays nothing.
8. **`[notifications] enabled`** in `config.toml`, default `true`, read at start; `false` answers `disabled` to every request.
9. **Every client kind may call `notification.show`**, under the `notifications` capability; a hook calls it through `umb`, so API §2's `cli` row gains it.
10. **Decision 15's notification**: a `pane.report_state` that changes the pane's effective state to `blocked`, or that changes which source decides a pane already `blocked`, raises title `<agent or source> needs you`, body the report's `message` or `pane <id> is waiting for you`, sound `request`, under the reporter's own `source` — so its rate limit is the reporter's. An unchanged or stale report raises nothing, nor do `done`, `working` and `idle`.
11. **Wiring**: `notify` stays a sink. `internal/api` implements its `Deliverer` and is handed to it after the server is built; `cmd/umbrald` adapts `integrations`' `Notifier` port to the sink. No module imports another.
12. **Delivery never waits on a client** (REQ-NTF-002, "without blocking other sources"). The sink counts under its lock and delivers after releasing it; `integrations` calls its notifier after releasing its own. Each interactive connection queues up to 16 notifications for its own writer; a full queue means the client stopped reading, and the answer is `no_client`. `shown` means handed to the foreground connection, not seen.
13. **`umb notify TITLE --source S [--body B] [--sound …]`** is how a hook raises one. It prints `shown` or `not shown: <reason>` and exits 0 either way: a notification not shown is an answer, not a failure.
14. **The TUI shows a notification on its status line**, as `title — body`. It plays no sound yet. `docs/qa/f1-tui.md` step 13 walks it.
15. **The daemon's own components** call the same `Show` under their reporter's source; there is no separate internal path until a component needs an `umbral:` source of its own.

Raised by the spec-guardian review on 2026-10-18: decisions 12 to 15, and the wording of 2 and 10.
