# Spike Q-01 — Is the libghostty snapshot replayable?

> Task `T-F0-04` · 2026-09-11 · REQ-TERM-004
> Library: `go.mitchellh.com/libghostty v0.0.0-20260908040635-9f448dfe8052`, linked against
> `libghostty-vt` built from ghostty `main` with Zig 0.16.0.
> Machine: AMD Ryzen AI 9 HX PRO 370, Linux, Go 1.27.1.

## The question

`session.subscribe` must hand a joining client the screen as it is now, before the live
stream starts (REQ-TERM-004). The plan's open question Q-01 was whether libghostty's
`Formatter` can produce **replayable** VT, styles and cursor included, or whether the
daemon needs the fallback: replaying the last N lines of the raw byte buffer.

## The answer

**Yes, with no fallback needed.** `FormatterFormatVT` emits VT that reproduces the screen
when fed to an empty emulator of the same size, and the formatter exposes the screen state
that text alone cannot carry, each behind its own option: palette, modes, scrolling region,
tabstops, working directory, keyboard, cursor, SGR style, hyperlink, protection, Kitty
keyboard and charsets.

Measured, on `internal/sessions/adapters/ghostty`:

| Property | Result |
|---|---|
| Plain text survives the round trip | yes, across all 8 golden cases |
| Cursor position survives | yes; dropping `WithFormatterExtraCursor` moves it to (11,1) instead of (0,2) |
| SGR, 256-colour and truecolour survive | yes |
| Wide characters and CJK survive | yes |
| Alternate screen survives | yes; mode 1049 is emitted and the replay enters it |
| Scrollback is included | yes; the formatter emits history, not just the active screen |
| The snapshot is a fixed point | yes; snapshotting the replay yields identical bytes |

`DD-001` stands as written. The Tech Design needs **no Delta**: the contract it describes,
daemon owns the VT state and clients render from bytes, is exactly what this supports.

## The finding that matters more than the answer

**`WithMaxScrollbackLines` alone does nothing.** libghostty holds a byte budget and a line
budget and prunes on whichever binds first, and its default byte budget is small. Setting
only the line limit leaves that default in place, so it wins.

Measured with 30,000 lines of 70 characters written to a 120-column terminal:

| Configuration | Lines retained |
|---|---|
| No option (library default) | 588 |
| `WithMaxScrollbackLines(10_000)` | 588 |
| `WithMaxScrollbackLines(100_000)` | 588 |
| `WithMaxScrollbackBytes(64 MiB)` | 30,000, that is, everything |
| **Both, `Lines(10_000)` + `Bytes(64 MiB)`** | **9,876** |

9,876 rather than 10,000 is the page-granularity approximation the library documents.

This is a silent failure, which is why it is worth this much space. Someone writes
`WithMaxScrollbackLines(10_000)`, the tests pass because test sessions are short, and users
lose 94 % of the scrollback the spec promises without any error anywhere. `NewTerminal` in
the adapter therefore sets both, and `TestScrollbackLimitNeedsBothBudgets` calls that
constructor rather than rebuilding the options, so deleting either one fails the suite.

## Cost, for T-F0-06

One snapshot per subscribing client, so the numbers below size the 8 MiB per-client queue.

| Screen | Snapshot | Time |
|---|---|---|
| 2 lines | 84 B | — |
| 100 lines, 24-row screen | 1.1 KiB | — |
| ~9,900 lines of 70 characters (the cap) | 704 KiB | 9.0 ms, 6 allocations |

`BenchmarkSnapshotFullScrollback` pins the last row.

Two consequences:

- **A full snapshot fits the 8 MiB queue with room to spare**, at under a tenth of it.
- **9 ms is not free.** REQ-TERM-006 budgets 5 ms p95 for *output* latency, which is a
  different path, but T-F0-06 must not build a snapshot on the goroutine that drains the
  PTY. Build it on the subscribing connection's own goroutine.

**The palette is opt-in.** `WithFormatterExtraPalette` costs a flat 5.5 KiB whatever the
screen holds: 5,606 bytes against 84 for a two-line screen, so 98 % of the message. The
adapter leaves it off and exposes it through `SnapshotOptions.Palette`, for the caller that
knows the program redefined the palette. That caller does not exist yet; T-F0-06 decides.

## Risk recorded

The bindings have **no tagged release**. The module resolves to a pseudo-version and its
README says plainly that API stability is not promised yet. The `Emulator` port in Tech
Design §4 is what contains this: everything above depends on the port, and this adapter is
the only file that names a libghostty symbol. Pin the pseudo-version in `go.mod`, which
`go get` already did, and treat an upgrade as a task rather than a routine bump.

## What was not tested

- **Resize.** Reflow on resize is REQ-TERM-002 and belongs to the conformance suite in
  T-F0-07, not here. Whether a snapshot taken before a resize replays correctly after one
  is a T-F0-06 question, since the answer there is to re-snapshot rather than to migrate.
- **Real PTY output.** Every case here is a hand-written byte string. A shell driving a
  real PTY arrives with T-F0-05.
- **macOS.** Built and measured on Linux only.

## Reproducing

```
task deps:ghostty                      # builds libghostty-vt with Zig into ~/.local/ghostty-vt
go test ./internal/sessions/adapters/ghostty/
go test -run '^$' -bench . ./internal/sessions/adapters/ghostty/
```

Golden files live in `internal/sessions/adapters/ghostty/testdata/`; `-update` rewrites
them.
