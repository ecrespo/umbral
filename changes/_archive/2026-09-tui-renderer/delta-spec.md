# Delta — the TUI's own VT renderer, and the packages that hold it

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-09-20 — folded into specs/` |
| **Date** | 2026-09-20 |
| **Task** | T-F0-12 |
| **Approved by** | Ernesto Crespo |
| **Raised by** | T-F0-12: the boundary rules have no room for the renderer DD-001 requires |

## Evidence

DD-001 already decides that the client renders for itself: "Clients receive raw bytes
(`session.output`) and process them with **their own renderer**", and the cost column of
the chosen option says so again — "double parsing (daemon and client)". T-F0-12's own
description says "session rendering from `session.output` (client-side emulator)".

What no artifact says is where that renderer lives. The §5.2 row for `tui`, added by
`2026-09-cli-surface`, reads "`client` and the `domain` packages; never `api`". The only
VT implementation in the repository is `internal/sessions/adapters/ghostty`, which belongs
to the `sessions` module and is out of reach.

It is not optional for this task. `session.subscribe` returns a VT snapshot and then VT
bytes, and REQ-TUI-001 requires **splits** — two sessions on screen at once. Bytes can be
handed to the host terminal only when one session owns the whole window; the moment two
do, something has to own a cell grid and place it.

## Decision

The TUI gets its own renderer, built on the same libghostty the daemon uses, in
`internal/tui/adapters/ghosttyvt`, behind a port in `internal/tui/ports`.

The alternative considered and rejected was a pure-Go VT implementation from Bubble Tea's
own ecosystem (`ultraviolet`, `x/ansi`), which would keep `umbral-tui` free of cgo and
easy to distribute. It was rejected because the daemon and the client parse the *same*
byte stream, and two different emulators parsing it are two chances to disagree: what the
user sees on screen and what T-F0-09 recorded in the block history would drift, and the
VT conformance suite of T-F0-07 covers only one of the two. Sharing libghostty makes that
class of bug impossible rather than merely unlikely.

The cost is accepted and recorded: `umbral-tui` now links cgo and needs libghostty-vt to
build, so `task deps:ghostty` is a prerequisite for the client as well as the daemon, and
the property that "one file names a libghostty symbol" becomes "one file per binary".

## MODIFIED

### specs/technical/umbral-architecture.md → §5.1

- **After:** `internal/tui/` is listed with its `ports/` and `adapters/`, like every other
  module.

### specs/technical/umbral-architecture.md → §5.2

- **Before:** `| tui | client and the domain packages; never api |`.
- **After:** that row, widened to include the TUI's own `ports`, plus two more:
  - `tui/ports` may depend on `sessions/domain`, for the size and cursor types the daemon
    already defines. `domain` imports nothing outside the standard library, so it is a safe
    shared kernel; inventing a second `Size` would have been the alternative.
  - `tui/adapters/**` may depend on its own `ports`, on `client`, and on `sessions/domain`,
    plus external libraries — the same rule every other module's adapters obey. The
    `client` allowance is what keeps method names and parameter shapes in one adapter
    instead of spreading them through the model.
  `tui` itself still may not reach into its adapters: `cmd/umbral-tui` wires them, as
  `cmd/umbrald` does for the daemon, and `cmd` gains `tui-ports` and `tui-adapters`.

### specs/technical/umbral-architecture.md → DD-001

- **After:** a consequence sentence naming which renderer the TUI uses and why it is the
  daemon's, so the next client author does not have to rediscover the argument.

### specs/technical/umbral-architecture.md → §3.2

- **After:** a row for `tui`, naming `charm.land/bubbletea/v2` and go-libghostty. §3.2 is
  where each component's technology is recorded, and the precedent is delta
  `2026-09-block-lifecycle-decisions`, which exists partly to note `klauspost/compress/zstd`
  as a `sessions` dependency. Bubble Tea's module path is unusual enough to be worth
  writing down: the library the specs call "Bubble Tea v2" is `charm.land/bubbletea/v2`,
  not a `github.com/charmbracelet/...` path.

### specs/constitution.md → stack table, and what this delta does *not* adopt

- **Before:** the TUI stack is fixed as "Bubble Tea v2 + Lip Gloss".
- **After:** unchanged, but recorded here: **F0 does not use Lip Gloss.** The layout the
  base TUI needs is two columns and a status line, which `joinColumns` does in forty lines
  against a dependency that would arrive with its own colour and border model. Dropping
  half a fixed stack decision is itself a decision, so it is written down rather than left
  to be noticed. Two consequences follow, and T-F0-14 is where they come due: column widths
  are measured in runes, which mis-measures wide and combining glyphs, and there is no
  styling layer for the pane borders the pane tree will want. Adopting Lip Gloss then is
  expected; this delta records only that F0 did not.

### .go-arch-lint.yml

- **After:** components `tui-ports` and `tui-adapters`, with the dependencies above. The
  rules are what `task arch` enforces; §5.2 is what they mean.

## NOT MODIFIED

No API, PRD or data-model change. `protocol_version` stays at 1. Nothing the daemon does
changes: it still owns the authoritative emulator, and the client still receives bytes.

## Verification

- `task arch` green, and `scripts/arch_selftest.sh` still proves the rules bite.
- The `tui` component's `mayDependOn` list contains no `sessions-adapters`, so an import
  from `internal/tui` into `internal/sessions/adapters/ghostty` is rejected. That follows
  from the rules rather than from a run: `scripts/arch_selftest.sh` demonstrates only the
  `sessions` → `agents` case.
