# Delta — a hosted agent's model traffic through a relay the daemon serves

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-11-20 by the Tech Lead ("Como recomiendas")`: the five open questions are answered as recommended, and the decisions stand as proposed with those answers. **Not implemented**: T-HRD-20a, b and c follow, each with its own tests; the requirements' final text is written into the PRD by the piece that makes each true |
| **Date** | 2026-11-19 |
| **Task** | T-HRD-20 |
| **REQ** | will add REQ-SBX-008 and REQ-SBX-009 (REQ-SBX-007 was taken by T-HRD-21 on 2026-11-20) and amend REQ-ACP-007 and REQ-ACP-009; none is in the PRD yet |
| **Raised by** | Delta `2026-11-agent-sandbox` Q1 option (b) and decision 8; delta `2026-11-open-questions` S1; delta `2026-11-full-auto-sandbox` decision 9 |
| **Depends on** | T-HRD-15, T-HRD-16, T-HRD-17 |

## Evidence

Measured on 2026-11-19 on Ubuntu 26.04.1, kernel 7.0 (Landlock ABI 8), OpenCode 1.18.33,
Ollama with `gpt-oss:20b`, through the spike's helper. The first three are from
`docs/spikes/2026-11-landlock.md`; the last three are new.

- Landlock's network rules cover TCP `bind` and `connect`, **by port, on every host**. UDP is
  not covered: OpenCode's DNS query left over UDP with every TCP port denied.
- `opencode acp` serves HTTP to itself: with every TCP port denied it does not start
  (`ServeError`).
- The agent reaches its model through an HTTP base URL, so the relay is an HTTP listener on
  a loopback TCP port and not a Unix socket — which the sandbox refuses anyway.
- **A first start in an empty home, TCP limited to its own port and the model's, completes a
  turn** (`end_turn`, 6343 input tokens). Its 48 connections to port 443, npm's registry,
  were refused and it went on: the provider package it needs ships inside it.
- **Two agents at once.** The second finds 4096 taken and binds port 0, an ephemeral port.
  With TCP limited to 4096 and the model's port, that bind is refused and the second agent
  does not start.
- **`opencode acp --port N`** exists (default 0, which tries 4096 first).

## Decisions proposed

1. **A relay per hosted thread.** `127.0.0.1:<R>`, served by the daemon, forwarding to the
   one provider endpoint the thread's agent is configured for. The agent's configuration
   names the relay as its base URL.
2. **The relay holds the key; the agent does not.** Today a manifest's `env_refs` hand the
   agent the provider's key. Behind the relay the agent is given a token of the thread's
   own, random, valid for that relay only, and the relay adds the real key. A hosted agent
   then never sees a secret of the user's. The token is also what stops any other local
   process from using the relay as an open door to the provider on the user's account.
3. **What is recorded.** A request to a non-loopback endpoint is an `egress_log` row, as
   Umbral's own calls are (Tech Design §4); one to a loopback model is none. The row says
   the thread, the provider, the host and the byte counts. The body is not stored and is
   not redacted: it is the agent's request, forwarded as it came. (Q2.)
4. **The network profile, Linux.** Where Landlock has network rules (ABI 4, kernel 6.7):
   `bind` and `connect` on the agent's own port, `connect` on the relay's, nothing else over
   TCP. The seccomp filter that already refuses `socket(AF_UNIX)` also refuses a datagram
   or raw socket of the Internet families, since Landlock cannot refuse UDP. Below ABI 4 the
   network is not confined and the thread says so.
5. **The network profile, macOS.** `(deny network*)`, then `localhost:<port>` allowed for
   the same two ports. Unlike Linux it names the host.
6. **Ports.** The daemon picks two free loopback ports per hosted thread and passes the
   agent's with the manifest's port flag (`--port` for OpenCode). A manifest without one
   cannot be given a network profile and its thread stays `unobserved`.
7. **`egress_visibility = confined`** for a hosted thread whose network profile the kernel
   enforces, as delta `2026-11-agent-sandbox` decision 8 has it; `unobserved` for the rest.
   Migration 0007 wrote the column's `CHECK` in an `ALTER TABLE`, which SQLite cannot
   change: the forward-only migration has to rebuild `threads`, the table every agent table
   holds a foreign key against. (Q3.)
8. **The disclosure** on a `confined` thread says: its model calls go through Umbral and
   are recorded; it reaches nothing else over the network — on Linux, nothing else over
   TCP but its own port **on any host**, and no UDP.

## Answers of the Tech Lead (2026-11-20)

Each question was answered as recommended.

- **Q1 — the port rule's reach on Linux: accepted and disclosed.** "Connect to port R" allows
  port R on every host, not only loopback, and the agent knows R. A network namespace is
  restricted for unprivileged users by Ubuntu's AppArmor, and seccomp cannot read
  `connect`'s address. The thread's disclosure says it (decision 8); macOS names the host
  and does not have the limit.
- **Q2 — the relay records and does not redact.** A request to a non-loopback endpoint is an
  `egress_log` row; its body is the agent's, forwarded as it came, neither stored nor
  redacted. The disclosure says so, and so does Art. 4's amendment of 2026-10-22, whose
  text T-HRD-20c changes with the code that makes it true.
- **Q3 — a new column, not a rebuilt table.** `threads` gains `network_profile` in one
  `ALTER TABLE … ADD COLUMN` with its own `CHECK`; `egress_visibility` on the wire is
  computed from both, and reads `confined` for a hosted thread whose network profile the
  kernel enforces. The stored `egress_visibility` keeps its two values and its `CHECK`.
  Decision 7 is read with this answer.
- **Q4 — the key stays with the daemon.** A confined agent behind the relay is not given the
  provider's key: its `env_refs` for it are withheld and the relay adds the key to what it
  forwards. An agent that uses its key for anything but its model endpoint stops working
  there; `[sandbox] mode = "off"` is the way back, and it is disclosed.
- **Q5 — it does not gate release 0.1.** The network of a hosted agent ships open and
  disclosed (S1 of delta `2026-11-open-questions`) until this lands.

## Tasks proposed

T-HRD-20 is three pieces, each mergeable on its own:

- **T-HRD-20a · The network profile.** `Profile.Net` (ports), Landlock's port rules, the
  datagram filter, the macOS rules; kernel tests for each; no caller yet.
- **T-HRD-20b · The relay.** `internal/acp` gains a port for it and `cmd/umbrald` an
  adapter over `llmgw`'s provider configuration: the listener, the token, the forwarder
  with streaming, the `egress_log` row.
- **T-HRD-20c · Wiring and disclosure.** The manifest's base URL and port flag, the ports
  per thread, the profile's use, `egress_visibility`, the migration Q3 decides, the clients'
  text, and the task's Done line against a real OpenCode.

## Constitution check

- **Art. 4:** this narrows what the 2026-10-22 amendment leaves undisclosed; by Q2's answer
  the amendment's text changes, in T-HRD-20c.
- **Art. 5:** a hosted agent would hold no secret (decision 2).
- **Art. 6:** a forward-only migration that adds one column (Q3's answer); no applied
  migration is edited.
- **Art. 9:** no new dependency: `net/http/httputil` and `golang.org/x/sys/unix`.
