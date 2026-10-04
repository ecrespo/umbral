# QA checklist — `umbral-tui` agent panel (T-F1-20)

The automated tests drive the model against fakes, and the model with its real adapter against a
real daemon and a scripted model. What they cannot cover is a real terminal and a real model:
whether your emulator sends ctrl+space and alt+a at all, how the panel reads at your font size,
and how a turn from a model that thinks out loud looks while it streams. That is what this list
is for, and REQ-TUI-001's agent half, REQ-TUI-002 and REQ-TUI-003 are only met once it has been
walked through.

## Setup

1. Build: `task deps:ghostty` once, then `task build`.
2. Have a model the daemon can reach. With Ollama on this machine, `gpt-oss:20b` pulled and
   `~/.config/umbral/models.toml` holding at least:

   ```toml
   [classes]
   code = ["ollama/gpt-oss:20b"]
   fast = ["ollama/gpt-oss:20b"]

   [[providers]]
   id = "ollama"
   type = "ollama"
   base_url = "http://127.0.0.1:11434"
   ```

3. Run `umbral-tui` from a real terminal — not through `script`, `tmux` or an editor's console.
   In its shell, `cd` into a scratch directory you do not mind the agent writing to, **then run
   one more command there** (`pwd` will do). The thread works where the pane's last command
   started. The `cd` itself started in the old directory, so it is the command after it that
   counts. Before the pane has run any command, the thread works where the session started,
   which is `$HOME`.

## Steps

| # | Step | What should happen | ✅ |
|---|---|---|---|
| 1 | Press ctrl+space | A panel opens on the right and the pane narrows; `tput cols` in the shell agrees with what you see. The status line says agent mode. | |
| 2 | Type `what files are here?` and press Enter | Nothing reaches the shell. The panel shows your line, the thread id, then the turn: "thinking", the `list_dir` call, and the answer streaming in. It ends with "turn ended: end_turn". | |
| 3 | Press ctrl+space, type `ls` and Enter | The keys go to the shell again; the panel stays visible. | |
| 4 | ctrl+space, then `create hello.txt containing hi` and Enter | The turn pauses: the panel shows `approve write_file (WriteFS): hello.txt` with `ctrl+y approve · ctrl+r for this thread · ctrl+l always · ctrl+x deny · ctrl+e diff`. `hello.txt` does not exist yet, in the scratch directory (check from another terminal). | |
| 5 | Type a few words starting with `a`, `d` or `l`, then press ctrl+e, then ctrl+y | The letters land in the input and answer nothing. ctrl+e shows the diff with `+hi`; after ctrl+y the file is written and the turn ends. | |
| 6 | Ask for another write and press ctrl+x | The file is not written; the model is told it was denied and says so. | |
| 7 | Ask it to run `sleep 30` with run_command, approve, then press ctrl+c | The turn stops within a second ("turn ended: cancelled") and no `sleep` is left running (`pgrep -f "sleep 30"`). | |
| 8 | In shell mode run `false`, press ctrl+b, select that block, press alt+a (with the block list closed, alt+a is the shell's) | The panel opens in agent mode with `@block:blk_… ` in the input. Type `why did this fail?` and Enter: the answer refers to the command and its exit code. | |
| 9 | With a turn waiting on an approval, switch to shell mode and type a command | The status line keeps saying the agent is waiting; ctrl+space brings you back to answer it. | |
| 10 | Press esc in agent mode with an empty input | The panel closes, the pane widens again, and keys go to the shell. | |
| 11 | Resize the window with the panel open | Panes and panel follow; the shell's `tput cols` agrees, and no row of the panel wraps or loses its last character. | |
| 12 | Ask for a write, and with the approval pending quit the TUI (ctrl+q); start it again | The approval is still listed and the panel opens on it; ctrl+x answers it. | |

## Known limits of this panel

These are not defects; they are the boundary of what T-F1-20 delivers.

- **One thread per TUI run.** The first message creates it, in the directory of the focused
  pane's last command; there is no thread list or switcher yet. Approvals, though, are every
  thread's: one a thread of another run is waiting on is listed and answered here.
- **ctrl+space is fixed.** There is no setting to change it yet.
- **Pasting into the shell** is not forwarded; pasting into the agent's input is.
- **No transcript scrolling.** The panel shows the tail of the turn that fits.
- **ctrl+space depends on the emulator.** It is sent as NUL by most terminals; one that does not
  send it cannot toggle the mode.
- **MCP servers and skills** in the panel are REQ-TUI-004, task T-F1-37.

## Result

| Field | Value |
|---|---|
| Walked through by | |
| Date | |
| Terminal and OS | |
| Model | |
| Steps failed | |
