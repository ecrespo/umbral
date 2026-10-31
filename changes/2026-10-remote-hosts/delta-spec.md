# Delta — remote daemons over SSH, and the relay to them

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing F2 instruction (F2b … F2f) |
| **Date** | 2026-10-31 |
| **Task** | T-F2-41 |
| **REQ** | REQ-RMT-001, REQ-RMT-002, REQ-RMT-003 (no change to their text) |
| **Folded into** | API 1.58 §2, §4 `RemoteHost`, §5.44, §6, §9; Tech 1.84 §3, §5.1 `[remote]`, §5.2, §5.3 (destructive list); Data Model 1.27 §2.14, §5; `specs/tasks/umbral-f2-tasks.md` (T-F2-41 done) |

## Evidence

- The PRD gives the command (`umb remote add NAME ssh://user@host`), the transport (a forwarded
  socket, never a TCP port) and the outcomes. It does not say how the remote socket and token are
  found, how the client addresses a remote object, or what a dropped link does to subscriptions.
- OpenSSH forwards a Unix socket to a Unix socket with `-L local:remote`. It binds the local
  socket at start and opens the remote one per connection, so `ExitOnForwardFailure` covers the
  local bind only: a forward that started says nothing of the remote daemon.
  `StreamLocalBindUnlink` replaces a stale local socket, and `StreamLocalBindMask` sets the local
  socket's mode.
- The daemon's ids are not globally unique. Workspace ids are `w<n>` (constitution Art. 6
  amendment), so two daemons both have a `w1`.

## Decisions

1. **The link.**
   - The local daemon runs `ssh … -- [user@]host umb remote endpoint`. On the remote machine that
     starts its daemon if need be and prints `{"socket", "token"}`.
   - It then runs `ssh -N -L <runtime>/remotes/<name>.<pid>.sock:<socket>`. The options are
     `BatchMode=yes`, `ExitOnForwardFailure=yes`, `StreamLocalBindUnlink=yes`,
     `StreamLocalBindMask=0177`, and `ServerAliveInterval=15` with `ServerAliveCountMax=3`.
   - Both calls take `ControlMaster=no` and `ControlPath=none`: a shared master would carry the
     forward past its `Close` and past this daemon. The endpoint call takes
     `ClearAllForwardings=yes`. The forward cannot take it, since it would clear its own `-L`, so
     a `LocalForward`, `RemoteForward` or `DynamicForward` the user's ssh config gives the host
     still applies to it; the "no TCP port" of REQ-RMT-001 is this daemon's, not the user's
     config's.
   - The host is `connected` only once a handshake with its daemon through the forward succeeds
     (`probeHost`, through `internal/client`, wired in `cmd/umbrald`). A failing probe is retried
     like a dropped link.
   - On Linux ssh is started with a parent-death `SIGTERM`, so a `kill -9` of the daemon leaves no
     forward. macOS has no equivalent: the forward lives until its link drops, and the next
     daemon's sweep removes its socket.
   - A name whose forwarded socket path would pass 103 bytes (macOS's `sun_path`) is refused at
     `add`.
   - The token crosses only that ssh channel, to a user who can already read it, and is kept in
     memory. It is asked for again at every link and reused by every relayed connection over that
     link; the token survives a restart of the host's daemon (`LoadOrCreateToken`).
   - `--` always ends ssh's options before the destination. User and host are checked so that
     neither starts with `-`.
2. **Configuration.** `[remote] ssh` and `[remote] umb` name the two programs, default `ssh` and
   `umb`. ssh hands `umb` to the remote user's shell, as it does with any remote command.
3. **Backoff.** A forward that ends, or a link that cannot be made, is `disconnected` with one
   line of reason (ssh's last stderr line). The next attempt waits 1, 2, 4, 8 or 16 s, then
   30 s each time, and the wait starts over once a link held. Each host has its own supervisor.
4. **The forwarded socket carries the daemon's pid.** During a live handoff (T-F2-40) the old
   and new daemons each forward the same host, and neither may remove the other's socket. At
   start the daemon sweeps the sockets of daemons that are gone.
5. **Addressing: ids are prefixed with the host's name.**
   - Through this daemon, every id a host hands out becomes `name/id`, and each `Workspace`'s
     label becomes `name: label`.
   - A request whose id members hold `name/…` for a known host is relayed to that host with the
     prefix taken off. So is a request whose `host` member names a host, which is how a client
     reaches a host when it holds none of its ids yet (`thread.create`, `workspace.list`).
   - Id members are `id`, `workspace` and every `*_id` and `*_ids`, at any depth. The exceptions
     are `client_msg_id`, `connection_id`, `trace_id`, `model_id` and `provider_id`.
   - This rule over names, not a per-method table, is what lets any method this build knows be
     relayed without a change here. A method it does not know is `METHOD_NOT_FOUND` before any
     routing, so a host's newer methods are not reachable through an older relay.
   - Two hosts' ids in one request are `VALIDATION_ERROR`, and so is a request routed by a
     prefixed id that also carries an unprefixed one: ids are not unique across daemons, and
     `w2:t1` beside `nas/w1:p1` would name nas's tab, not the local one the client holds. Under a
     `host` member every id is the host's as written. A prefix that names no host is left for
     this daemon to judge, and a `host` that cannot be a name is `VALIDATION_ERROR`.
   - A `Model`'s `id` is prefixed in a relayed answer like any `id`; `model_id` is not stripped
     on the way up, so a client names a host's model by the host's own catalog id.
6. **One upstream connection per client connection and host.**
   - It is opened on first use and authenticated with the host's token and the client's own
     kind, so the host enforces its own §2 allowlist.
   - This daemon checks the kind and the agent's-terminal rule (REQ-ORC-004) before it relays.
     Otherwise a turn could have a host land its work just by naming the host
     (`TestTheRelayIsNoWayAroundTheAgentCheck_REQ_ORC_004`). The host's own check sees sshd as
     its peer and cannot apply, so the `userOnly` list that protects a host through the relay is
     this daemon's; a host on a newer build with a longer list is protected by the older list.
   - Relayed notifications take this daemon's envelope `seq` and go through the connection's
     control queue, output included, so §8's limit closes a client that stops reading a remote
     session as it does for a local one. A host's `remote.*` and `system.*` notifications are
     dropped: they are about the host's own links and connection, and a host's report about its
     own remote named `x` would read as this daemon's `x`.
   - The dial and the handshake run outside the connection's relay lock, bounded by the request's
     context; when two callers open one host's link at once the first stored wins. A link that
     finishes opening after its client connection closed is closed, never stored.
   - Closing a client connection closes its socket first and then its links, and a link ending on
     a closing connection writes nothing: the close may run on the fan-out's goroutine
     (REQ-API-006), which must never wait on a stalled client's writer.
   - Nothing of a host's is decided, recorded or stored here (REQ-RMT-003).
7. **`session.snapshot` merges.** A snapshot without `host` returns this daemon's tree and then
   each connected host's tree, prefixed. `seq` and `focused` are this daemon's. The hosts are
   asked at once; a host that does not answer within 5 s is left out of that snapshot.
8. **A dropped link.**
   - Each session subscribed through the link gets `session.unsubscribed` with
     `reason: "remote_disconnected"`.
   - Each call waiting on the link gets `PROVIDER_UNAVAILABLE`, as does a request for a host that
     is not connected.
   - `remote.state_changed` announces the host's state.
   - The relayed connection can end while the ssh link stays up: the host's daemon restarted,
     handed its panes over (T-F2-40) or closed the relay. The remote module then announces
     nothing, so the relay redials that host with the same backoff and sends the connection that
     lost it, alone, `remote.state_changed` `connected` once a new connection holds
     (`TestAHostDaemonRestartBehindALiveLinkIsFollowed_REQ_RMT_002`). When the ssh link is down too
     it stops, and the module's own announcements follow; a client may see `connected` twice.
   - A request whose write to the host fails is answered once, with that error.
   - The TUI and the desktop subscribe that host's panes again when the host is `connected`, and
     the fresh snapshot is the resume (REQ-RMT-002).
   - `umb remote add` and `umb remote remove` are destructive patterns (`umb_remote_change`),
     beside the daemon's refusal to an agent's terminal, as T-HRD-07 did for `umb mcp remove`.
9. **Schema.** Migration `0009_remotes` holds name, URL and `created_at` only. The link state is
   in memory.

## Not done here

- A request to a host whose link is being opened runs on the client connection's read loop, so
  one slow host can hold that connection's other requests for up to the 5 s dial and 10 s
  handshake. Running relayed requests off the read loop would reorder them against local ones.
- The desktop's resubscription on `remote.state_changed` has no automated test; the TUI's has
  (`TestARemoteHostsPanesWaitForItAndResume_REQ_RMT_002`).

- The TUI does not show the workspace tree. A remote host's workspaces reach it through the
  snapshot, and its own panes are its own sessions. The desktop shows them as `name: label`.
- A remote daemon that serves other remotes is reached through them with nested prefixes
  (`a/b/w1`), but no test covers it.
