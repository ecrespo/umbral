# Delta — how the MCP client names, guards, reaches and recovers a server

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-01 — approved by E. Crespo as written. Folded into PRD 1.17, Tech Design 1.33, Data Model 1.16 and the F1 tasks file (API 1.25 was written by the task)` |
| **Date** | 2026-10-01 |
| **Task** | T-F1-17 |
| **Raised by** | T-F1-17: REQ-MCP-001…004 and API §5.27 leave the decisions below to the implementation |

## Evidence

- **REQ-MCP-001** gives the prefix `mcp_<server>_<tool>`. The registry only accepts names that
  match `[a-z][a-z0-9_]{0,63}`, and servers name tools with `-`, `.` and upper case.
- **REQ-MCP-004** says MCP tools "ask by default". The policy engine decides by risk class, and
  an MCP tool has no risk of its own. The policy reads an `Exec` target as a shell line, so a
  per-tool allow rule can never match an `Exec` MCP tool whose target is JSON (found by
  `TestMcpDefaultAsk_REQ_MCP_004`).
- **REQ-SEC-006** names the results of `trust = untrusted` servers as untrusted content.
  Nothing said whether a failure the server reports (`isError`) is a result or an error. An
  error message carries no taint.
- **Art. 4** applies to "every request that leaves the machine". An `http` server can be on
  another machine, and its requests are neither redacted nor logged by anything existing.
- **REQ-MCP-003** gives a 10 s limit and "exponential backoff (at most 5 attempts)", but no
  base or factor, and no rule for what happens to a server's tools meanwhile.
- **API §5.27** wrote only `add`'s params. It had no results, no `list`/`remove` params, and no
  error for a duplicate name or an unknown one. The `tools/api_schema_check.py` checker dropped
  every method named in full after the first one in a heading, so `mcp.server.add` and
  `mcp.server.remove` were compared with nothing.
- **Tech §5.2** has no `mcp` row, although §5.1 lists the module.

## Decisions

1. **Names.** A tool is offered as `mcp_<server>_<tool>`, lower-cased, with every character of
   the tool's name outside `[a-z0-9_]` folded to `_`. A name over 64 characters is not offered;
   it is never truncated into a collision. A server's `name` is 1–32 characters of `a-z` and
   `0-9`, narrower than the Data Model's CHECK. With `_` or `-` allowed, `mcp_github_*` would
   also cover a server `github_enterprise`, and `a-b` and `a_b` would fold into one prefix
   whose tools went to whichever registered first.
2. **Every MCP tool is `Network`**, whatever its transport and whatever annotations the server
   gives it. What the tool does happens outside Umbral's view, as a remote request does. So:
   - it asks by default in `normal` and `auto-edit` (REQ-MCP-004);
   - a tainted turn asks for it even under an allow rule (REQ-SEC-006);
   - `ask` mode does not offer it (REQ-AGT-009).

   The target the rules match is the call's arguments as one line of compact JSON. A rule
   `mcp_<server>_*` covers a whole server, and a rule naming one tool covers that tool.
3. **Taint.** The results of an `untrusted` server are tainted. Anything carrying the server's
   text reaches the model as a result, tainted like any other, and never as an error message,
   which would carry that text with no taint:
   - a failure the server reports (`isError`) begins `[the server reported that the call
     failed]`;
   - a protocol error, whose message is the server's verbatim, begins `[the server returned an
     error]`.

   Only the daemon's own words stay errors: `unavailable`, a call refused for a secret, and a
   cancel.
4. **Art. 4 for `http` servers off this machine.** "Off this machine" means any host other than
   `localhost` or a loopback address.
   - Every HTTP request to such a server is recorded in `egress_log` before it is sent, failing
     closed. The row has `provider = mcp:<name>`, the thread when the request is a call, and
     the body's SHA-256 and size.
   - A call whose arguments the redaction rules would change is refused and nothing is sent, as
     `fetch_url` refuses a URL.
   - A loopback server and a stdio server are not egress. A redirect is followed only to the
     same host, so a loopback server cannot relay a call off the machine unrecorded.
   - The `egress_log` row of an MCP request has `provider = mcp:<name>` and hashes the request
     body. A request with no body hashes its URL, as `fetch_url`'s rows do.
   - A remote server takes no credential in F1: `http` servers have no `env_refs` and no
     headers, and a `url` with userinfo, or one the redaction rules would change, is
     `CONFIG_INVALID`, because it would be stored in clear (REQ-SEC-004). Authenticating a
     remote server is left to a later delta.
   - `router.offline` governs model providers only, and does not disable an MCP server. Every
     MCP server is one the user configured by name, and every request to a remote one is logged.
5. **Timing and recovery (REQ-MCP-003).**
   - Connecting and listing the tools must finish within 10 s.
   - A call has 10 s. Past that it fails, the server is marked `unavailable` with "did not
     respond", and its session is closed. For a stdio server, that ends the process.
   - A call the turn cancels is not the server's fault, and leaves the server connected.
   - A lost or failed connection is retried after 1, 2, 4, 8 and 16 s, five attempts. After
     the fifth failure the server stays `unavailable` until it is removed and added again or the
     daemon restarts. Only a connection that stayed up for a minute resets the count. One that
     drops sooner spends an attempt, as one that never connected does, so a server that crashes
     at start is not retried forever.
   - While a server is unavailable its tools stay registered, and a call to one fails at once
     with "unavailable", which the model reads. Removing the server takes them out.
   - Every state change is persisted, then published as `mcp.server_state` (DD-007).
   - A server's tool list is read at each connection. `notifications/tools/list_changed` is not
     followed in F1, so a server that changes its tools is seen at its next reconnection.
   - A tool removed mid-turn — by `remove`, or by a reconnection that lists fewer tools — is an
     unknown tool to a call already planned: it is repaired once like any invalid call, and it
     counts in `umbral_tool_calls_invalid_total` (REQ-AGT-006, REQ-OBS-002).
6. **The stdio process.**
   - It inherits only `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`, `LC_CTYPE`,
     `TMPDIR`, `TZ` and the four `XDG_*` directories from the daemon, plus its resolved
     `env_refs`. A key the env fallback put in the daemon's environment does not reach it.
   - It runs in `$HOME`, and its stderr is discarded (Art. 7).
   - On remove and on shutdown its stdin is closed, and it gets SIGTERM 2 s later if it has not
     exited.
7. **`env_refs`.**
   - Only a `stdio` server takes `env_refs`, as `keyring:<path>` or `env:<VAR>`.
   - Anything else is `CONFIG_INVALID`, without the value. So is `env:` outside the keyring
     fallback (REQ-SEC-012).
   - References are resolved at each connection, as provider credentials are. One that cannot
     be resolved leaves the server `unavailable`, with the variable and the reason in
     `last_error`, and the process is not started.
   - REQ-SEC-012's degraded mode is reported. A server connected with an `env:` reference
     carries `degraded: <VAR>: env_secret` in `last_error` while it is `connected`, and
     `mcp.server.list` and `mcp.server_state` show it.
8. **Bounds.**
   - A tool's description is capped at 1024 characters, because it is the server's text in the
     model's prompt.
   - A result is capped at 256 KiB. Content that is not text appears as a placeholder, and
     structured content is used when the result has no text.
   - A tool whose input schema is not an object schema is not offered. A connected server's
     `last_error` lists the tools it did not offer, and why.
9. **API §5.27.**
   - `list` takes `{}` and returns `{items}`, by name, with the live state and the tools under
     the server's own names.
   - `add` returns the server `connecting` and connects it in the background.
   - `remove` takes `{name}` and returns `{}`.
   - The errors gain `CONFLICT`, for a name already taken, and `NOT_FOUND`, for an unknown name.
   - The methods are for interactive clients only until T-F1-37 gives them to `cli`.
   - `system.status` lists every server's `{name, state}`.
   - No method sets `disabled` in F1.
10. **Tech §5.2, a row for `mcp`.** "`mcp`: its own `ports` and `domain`, plus `bus`;
    `tools/adapters` may use `mcp/domain`, for `mcptools`, which offers a server's tools;
    `api` may use the `ports` and `domain` of `mcp`; only `cmd/*` wires the manager to the
    registry." `.go-arch-lint.yml` encodes this row.

## What changes in the specs on ratification

- **API §5.27:** written by T-F1-17 (API 1.25); decision 9.
- **Tech §5.2:** the `mcp` row of decision 10.
- **Tech §5.3:** a line saying MCP tools are `Network`, with their arguments as the target
  (decision 2).
- **Tech §3.3, error flow:** an item for decision 5.
- **Tech §6.1:** the "Malicious MCP server" row also names decisions 3, 4 and 6.
- **PRD REQ-MCP-003:** "(1, 2, 4, 8 and 16 s; only a connection up for a minute resets the
  count)" after "exponential backoff".
- **Data Model §2.12:** `egress_log.provider` also takes `mcp:<name>` (decision 4).
- **Data Model §2.13:** the application narrows `name` to `[a-z0-9]{1,32}` (decision 1); the
  CHECK is left as it is, because migrations are forward-only.
- **`specs/tasks/umbral-f1-tasks.md`:** T-F1-17's Files line (amended by the task).
