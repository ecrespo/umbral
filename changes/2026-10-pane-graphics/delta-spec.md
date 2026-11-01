# Delta — Pane graphics for clients that ask for them

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing F2 instruction; pending the Tech Lead's review |
| **Date** | 2026-10-31 |
| **Task** | T-F2-61 |
| **REQ** | REQ-PLG-003 |
| **Folded into** | API 1.61 (§2, §5.1, §5.11, §9), Tech 1.87 (§5.3k) |

## Evidence

- REQ-PLG-003 (SHOULD): WHERE a client advertises `graphics`, pass Kitty graphics protocol
  images from a pane to it, and replace them with a placeholder for clients that do not.
- API §9 reserved `graphics`, but nothing said how a *client* advertises anything: `system.hello`
  carries only the daemon's capabilities, in its result.
- The daemon streams a pane's bytes untouched (§5.11) to every subscriber, and the snapshot is
  replayable VT. Neither the TUI nor the desktop draws images: both render text through
  libghostty, the desktop as styled runs (Tech §5.3h).

## Decisions

1. **A client asks.** `system.hello` gains an optional `capabilities: string[]`, the client's own.
   `graphics` is the only value read; an unknown one is ignored, so a newer client meets an older
   daemon. The connection keeps the answer for its life.
2. **The daemon advertises `graphics`** whenever it serves `sessions`. It is not a namespace: no
   method sits under it; it says this daemon passes or filters a pane's graphics on request.
3. **What is filtered.** For a connection that did not ask, a filter in `sessions/domain`
   removes every Kitty graphics escape, `ESC _ G <control> [; <payload>] ESC \`, from that
   connection's `session.output` and from its `session.subscribe` snapshot. Other APC, OSC, DCS
   and CSI sequences pass untouched.
4. **The placeholder** is the text `[image]`, written where an escape displays an image: `a=T`
   once its last chunk (`m=0` or no `m`) is in, or `a=p`. A transmission (`a=t`, the default), a
   query, a deletion, a frame or animation command leave nothing. A placement with `U=1` leaves
   nothing either: its image is shown through Unicode placeholder cells that are ordinary text
   following it, which reach the client as they are.
5. **Streaming.** The filter is a byte-level state machine kept per subscription, so an escape
   cut by a PTY read or a batch boundary is recognised. It buffers no payload; at most an `ESC`
   or `ESC _` waits for the next byte, and at most 256 bytes of control data are kept. An `ESC`
   inside a graphics escape that is not `ST` ends it, as a terminal reads it, and CAN or SUB
   cancels it, leaving nothing in its place, as the VT parser libghostty follows does. A key
   written past the first 256 bytes of control data is not read. A batch that held only graphics
   sends no notification; the next one's higher `seq` is all a client checks, and a batch with no
   escape passes without a copy.
   **Where the filter starts.** A subscription's filter starts outside any escape. If the chunk
   the snapshot ends on left a graphics escape open, the rest of it reaches a client without
   `graphics` as text. Starting from the session's own filter state would need `sessions` to
   run a filter per session at ingest for clients that may never connect; this task does not,
   and the case is a reconnect landing inside an image's transmission. `[image]` takes up its
   own cells, so such a client's cursor may drift from the emulator's until its next snapshot.
6. **Clients that ask get the bytes as they came**, snapshot and stream. The snapshot is the
   daemon's emulator's, so whether it holds an image is libghostty's business; nothing is added.
7. **The relay** (§5.44) says `graphics` in its hello to a host exactly when its client did, so
   a host's panes are filtered for the same clients as local ones.
8. **Not filtered:** stored block output (`block.get` raw), which is a record and not the pane's
   stream; and input. Neither the TUI nor the desktop asks for `graphics` in this task: neither
   draws images, so both get the placeholder.

## Not in this task

- Drawing images in the TUI (passing them to the host terminal) or in the desktop.
- Sixel and iTerm2 inline images, which REQ-PLG-003 does not name.
