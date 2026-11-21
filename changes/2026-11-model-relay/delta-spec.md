# Delta — a hosted agent's model traffic through a relay the daemon serves

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-11-20 by the Tech Lead`, twice: the five questions as recommended ("Como recomiendas"), and then the section "What piece a built, and what it found" ("ratifico el delta 2026-11-model-relay") — **except its `listen` limit, which is not accepted and is to be closed** (see "The Tech Lead on the `listen` limit"). Pieces a and a2 are implemented — **a2 cannot be enforced on macOS, where a profile that binds is refused; whether to accept that limit instead is an open question for the Tech Lead** —; a2's section is the implementing agent's under the Tech Lead's instruction of 2026-11-20 ("auto aprueba cualquier delta que aparezca e implementalo"); b and c are not |
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

## What piece a built, and what it found (2026-11-20)

`Profile.Net`, on both systems, with no caller. Three things differ from decisions 4 and 5
as proposed, each stricter, and one limit is new.

- **The socket filter is an allow list.** Decision 4 refused "a datagram or raw socket of
  the Internet families". Landlock's rules are TCP's alone, and a stream need not be TCP:
  `socket(AF_INET, SOCK_STREAM, IPPROTO_MPTCP)` is one. Under a network profile the only
  sockets left are a TCP one of either Internet family and a netlink one of the routing
  protocol, which a resolver and `getifaddrs` open and which sends nothing off the machine;
  every other type, protocol and family is refused. Measured.
- **macOS opens a port to TCP only**, by `tcp` filters: an `ip` filter opens the same port
  to a datagram.
- **macOS leaves out six Mach services** under a network profile: the resolver's, the
  network's configuration and the trust daemons'. Each reaches the network for its caller,
  and a name resolved is a name sent out. A confined agent therefore resolves nothing and
  checks no certificate; it reaches its relay by address, over the loopback.
- **New limit, Linux: `listen` without a bind.** A socket that listens without a bind is
  given a port by the kernel, with no bind for a rule to refuse, and Landlock has no right
  for `listen` — none in kernel 7.0's header either. Measured: the confined probe listened.
  The filter refuses `listen` to a profile with no port to bind. A hosted agent binds its
  own port, so it keeps `listen`, and can receive connections on a port of the kernel's
  choosing, on every interface: a way in for whoever can reach the machine, and a way out
  only over a connection someone else opened. It joins Q1's limit in the disclosure
  (decision 8). **For the Tech Lead:** whether that is acceptable as disclosed, as Q1's
  was; the alternative found is to have the daemon listen on the agent's port and hand the
  agent the socket, which no agent's manifest supports today.
- **A profile with a network is refused wherever it cannot be enforced**, a system where the
  sandbox enforces nothing included. A profile without one runs there as it came
  (REQ-SBX-003); one that names a network does not, so piece c cannot launch an agent it
  believes cut off and is not. Piece c must also give a confined agent no descriptor but its
  pipes: the filter does not stop a send on a socket the process inherits.
- A TCP fast-open send, which connects without a `connect`, is refused by the kernel
  (measured on 7.0), and the test holds it.
- Found by the commit's automated security review and fixed before the pull request: the
  `listen` gap, the `ip` filter and the Mach services.
- Not measured: macOS, by its runner only; a kernel between 6.7 and 7.0, where a fast-open
  send may not be refused; whether macOS's directory service resolves a host name for its
  caller.

## The Tech Lead on the `listen` limit (2026-11-20)

"Pienso que puede ser un problema de seguridad, debería blindarse para no aceptar
comunicaciones externas." The limit is not disclosed and lived with: a confined agent is to
accept no connection from outside the machine. It is a piece of its own, before b and c,
since it changes how the helper runs a process with a network.

- **What must hold.** A process with a network profile listens only on a loopback address
  and only on a port its profile lets it bind. That closes two things, not one: the socket
  that listens without a bind, and a bind on `0.0.0.0:<its port>`, which Landlock's rule
  allows since it names no address.
- **How, on Linux.** Neither Landlock nor a seccomp filter can see what a socket is bound
  to. `listen` is handed to a supervisor through seccomp's user notification: the helper
  stays as the confined process's parent, takes the socket itself (`pidfd_getfd`), reads
  what it is bound to, and performs the `listen` on that same socket or refuses it. It
  never lets the call continue in the process, where another thread could swap the
  descriptor between the check and the call. A process that is no longer the supervisor's
  descendant is refused.
- **macOS** names the loopback in its rules already; whether a listen without a bind is
  refused there is measured by the runner.
- **REQ-SBX-008** loses its sentence on connections arriving and gains the rule above, in
  the change that makes it true.

## What piece a2 built (2026-11-20)

The supervisor, on Linux, as the section above describes it, and the tests first.

- **Measured before:** a process confined with a port to bind listened with no bind, on
  `0.0.0.0:<its port>`, on `[::]:<its port>` and on the machine's outward address.
  **After:** each is `permission denied`; the loopback's allowed port, of either family,
  listens and accepts a connection, from any thread of the process.
- **Two stages of the helper.** A process cannot be given a seccomp filter from outside, and
  the filter's notification descriptor has to reach a process that is not under it. The
  helper starts itself again with one end of a socket pair; the second stage confines
  itself, sends the descriptor up, closes both and becomes the command. The stage is told by
  a variable, removed before the command runs; set by anyone else it only makes the helper
  fail, since there is nothing to send the descriptor to.
- **What the caller sees is unchanged:** the helper's output and exit code are the
  command's, and killing the helper kills the command (`PR_SET_PDEATHSIG`). The command is
  now the helper's child, not the helper itself: the pid the daemon holds is the
  supervisor's, in the same session.
- **The rule** is a loopback address — `127.0.0.0/8` or `::1`, not an address mapped from
  the other family — on a port of the profile's `bind`. A socket with no bind has port 0
  and is refused.
- **Fails closed.** The helper ends with its command; a process left behind then gets
  `ENOSYS` from `listen`. **And it cannot become its own supervisor**: spec-guardian's
  review of this piece had such a process install a filter of its own with a notification
  descriptor — the kernel asks the newest filter that notifies — answer its own `listen`
  with "go on", and listen on every interface. The profile's filter now refuses `seccomp`
  asked for that descriptor, at any time; a refusal outranks a notification whatever is
  added later. Reproduced red, then fixed.
- **Descendants only.** The supervisor refuses a caller that is no longer below it — a
  process whose parent exited while the supervisor runs. Yama's ptrace scope 1, Ubuntu's
  default, already made that so, since only a descendant's descriptors can be taken; the
  check makes it the same under scope 0. A process that is not dumpable is refused, and
  under scope 2 or 3 every `listen` is, so there a confined agent cannot listen at all;
  piece c reports that when it finds it.
- **What killing the helper ends** is the command itself (`PR_SET_PDEATHSIG`). What the
  command started is ended by the launcher's reaper of the helper's session, as before:
  the pid the daemon holds is still the session's.
- **For piece c, before a terminal uses it:** a command killed by a signal is reported as
  exit 128 + signal; six termination signals are forwarded and the stop, continue and
  window ones are not; on a terminal the helper would be the session's leader.
- **The supervisor is not confined.** It reads one descriptor number from the command and
  nothing else. A confined process cannot trace or signal it: Landlock refuses a process
  outside its domain, and from kernel 6.12 its signals too.
- **Not measured:** the descriptor swapped by another thread between the look and the call
  — closed by construction, since the supervisor listens on its own duplicate, and with no
  test; a process whose threads do not share descriptors; a kernel between 6.7 and 7.0.
  The check that the notification is still its caller's (`NOTIF_ID_VALID`) has no test: the
  one mutation of seventeen that survives on its own; the descendants check survives in
  the kernel test wherever Yama already refuses, and is held by a test of its own.
- **macOS, measured on its runner (2026-11-21): the rule does not hold there.** A listen
  with no bind is refused, but a listen on `0.0.0.0:<its port>` is not, and a connection to
  the machine's outward address reaches it. `sandbox-exec` checks no host in a `local`
  filter, and checks inbound at `listen`, which has no peer, not at each connection
  accepted: a rule that requires a loopback peer (`require-all … (remote tcp
  "localhost:*")`) refused the listen itself, and allowing the port, refusing every peer
  and letting the loopback's back in listened and still let the outside connection
  through. Seatbelt has no rule that holds it, and there is no seccomp-like hook for an
  unprivileged supervisor.
- **What the code does: it fails closed.** A network the kernel cannot enforce is refused,
  as REQ-SBX-008's IF clause says, so on macOS a profile that binds is refused
  (`ErrNoNetwork`) and only a profile that connects is rendered. The Tech Lead's decision
  above — "a confined agent is to accept no connection from outside the machine" — is
  kept, not reopened. Today nothing gives a thread a network profile; piece c will meet
  this as a hosted OpenCode on macOS that cannot be given one, since it does not start
  without its own port.
- **Open question for the Tech Lead (Q-a2-mac).** Keep the refusal, or accept the macOS
  limit and render the binding profile — then a listener on every interface would be
  reached from outside, but every other use of the network would stay refused, where an
  unconfined agent has all of it. Other ways out, none tried: hand the agent a socket
  the daemon bound on the loopback, if OpenCode takes one; pre-bind the wildcard so the
  agent's fails, which a bind to the outward address still escapes; a `pf` anchor or a
  network extension, which need root or an entitlement.

## Tasks proposed

T-HRD-20 is three pieces, each mergeable on its own:

- **T-HRD-20a2 · No connection from outside.** The supervisor for `listen`, above; kernel
  tests that a listen without a bind and a listen bound to every address are refused and
  one on the loopback's allowed port works.
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
