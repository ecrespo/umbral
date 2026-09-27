# QA checklist — `umbral-tui` (T-F0-12)

The automated tests cover the model against fakes and the renderer against libghostty.
What they cannot cover is a real terminal: raw mode, the keyboard as your emulator sends
it, redraw under load, and how a split looks at your font size. That is what this list is
for, and REQ-TUI-001's F0 half is only met once it has been walked through.

Build first: `task deps:ghostty` once, then `task build`. `umbral-tui` links cgo, so
without libghostty-vt it will not build.

Run it from a real terminal — not through `script`, `tmux` or an editor's console.

| # | Step | What should happen | ✅ |
|---|---|---|---|
| 1 | `umbral-tui` with no daemon running | It starts `umbrald` and shows a shell prompt within a second or two. `umbrald.log` in the runtime directory holds the daemon's startup lines. | ✅ |
| 2 | Type `ls` and press Enter | The output appears as it would in any terminal. | ✅ |
| 3 | Run something interactive: `vim`, `htop`, `less /etc/services` | The alternate screen works, arrow keys move, and quitting returns to the prompt with the screen intact. | ✅ |
| 4 | Run something long and interrupt it: `sleep 30`, then Ctrl-C | The command dies. Ctrl-C must reach the shell rather than the TUI. | ✅ |
| 5 | Press Ctrl-B | The block list opens on the right, newest command first, each with its exit code. | ✅ |
| 6 | Press Ctrl-P a few times, then Ctrl-G | The selection walks to older blocks and back. At either end it stops and the status line says so. | ✅ |
| 7 | Press Ctrl-S | A second pane appears beside the first with its own shell. Both are narrower, and both shells know it: run `tput cols` in each. | ✅ |
| 8 | Press Ctrl-O | Focus moves between the two panes; typing goes to the focused one only. | ✅ |
| 9 | Press Ctrl-T, then Ctrl-N | A new tab opens with its own session; Ctrl-N cycles back round. | ✅ |
| 10 | Resize the terminal window | Panes follow. Run `tput cols` again: the shell agrees with what you see. | ✅ |
| 11 | Make the window very small (under ~25 columns) | It clamps rather than failing; the daemon is never sent a size it would reject. | ✅ |
| 12 | With the TUI running, `pkill umbrald` from another terminal | The tab bar says "disconnected from umbrald" and the last screen stays visible rather than going blank or hanging. | ✅ |
| 13 | Quit with Ctrl-Q | The terminal is left as it was: your shell's scrollback is intact and no escape sequences are left on screen. | ✅ |
| 14 | `umb block last --json` from another terminal | It reports a command you ran in the TUI, with the right exit code. The daemon's history and the screen agree. | ✅ |

## Known limits of the F0 client

These are not defects; they are the boundary of what T-F0-12 delivers.

- **Two panes per tab.** The pane tree, portable layouts and the `w1:t1:p2` identifiers
  arrive with T-F0-14 and T-F0-15.
- **No mouse, no function keys, no Kitty keyboard protocol.** `keyBytes` handles what a
  shell needs; the rest waits for something that tests it.
- **No agent panel.** REQ-TUI-001's other half is T-F1-20, as the traceability matrix says.
- **No scrollback view.** The pane shows the screen. `umb block last` and `block.search`
  reach the history.
- **Reconnection is manual.** A lost daemon is reported, not retried; restarting the TUI
  re-attaches to the sessions, which survive because the daemon owns them (REQ-TERM-003).

## Result

| Field | Value |
|---|---|
| Walked through by | Ernesto Crespo |
| Date | 2026-09-20 |
| Terminal and OS | Ubuntu 26.04.1 LTS, kernel 7.0.0-31-generic. Terminal emulator not recorded. |
| Steps failed | None. All 14 steps behaved as described. |

This table is the walker's attestation, not a machine result: nothing above can be asserted
from a test run, which is why the task waited for it. The terminal emulator went
unrecorded, which matters for steps 3, 4, 10 and 13 — the ones whose behaviour is the
emulator's as much as the client's — so a second walk on a different emulator is still
worth doing before release, and a failure there is a finding rather than a contradiction
of this one.
