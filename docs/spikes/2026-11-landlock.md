# Spike — OpenCode and a `full-auto` shell under Landlock (T-HRD-14)

| Field | Value |
|---|---|
| **Date** | 2026-11-19 |
| **Task** | T-HRD-14 (delta `2026-11-agent-sandbox`, decision 5; delta `2026-11-open-questions`, S1 and S3) |
| **Machine** | Ubuntu 26.04.1, kernel 7.0.0-43, Landlock ABI 8, AMD Ryzen AI 9 HX PRO 370 (24 threads) |
| **Tools** | OpenCode 1.18.33, Go 1.27.4, npm 11.19.1 (Node 24.22.1), git 2.53.0, bash 5.3.9, zsh 5.9 with oh-my-zsh, powerlevel10k and starship 1.22.1, fish 4.2.1 |
| **Product code** | none. The measurements used a throwaway helper of about 200 lines, kept out of the repository |

## How it was measured

A helper restricted itself with Landlock and then ran the command, as decision 2's re-exec
helper will. Every run was traced with `strace -f`, and the paths the kernel refused with
`EACCES` were read from the trace.

The bench copied Umbral's layout:

- a data directory holding a database file and `worktrees/`, with two worktrees of one
  repository, `thrA` (the confined thread's) and `thrB` (a sibling's);
- a runtime directory holding a token file and `acp/thrA/`, the agent's isolated home;
- the repository's main copy, outside both.

Umbral puts worktrees inside its data directory (`cmd/umbrald/worktrees.go`) and a hosted
agent's home inside its runtime directory (`cmd/umbrald/acp.go`). So the profile denies both
directories and allows the thread's own two back.

The base profile was decision 5 as written: write to the worktree, a private temporary
directory and the agent's home; read everything except the data and runtime directories.

## What decision 5 broke, and what each took

| Workload | Base profile | What it took to run |
|---|---|---|
| Any command that redirects to `/dev/null` | fails: `cannot create /dev/null: Permission denied` | write on `/dev/null`. The same holds for `/dev/zero`, `/dev/full`, `/dev/tty`, `/dev/ptmx` and `/dev/pts` |
| `git commit` in the worktree | fails: `could not open '/dev/null'`, then `Unable to create '<main>/.git/worktrees/thrA/index.lock'` | write on four paths of the **main copy's** `.git`: `worktrees/<name>`, `objects`, `refs` and `logs` |
| `go build` | builds, without VCS stamping and without a cache; every unit is recompiled | write on `~/.cache/go-build`; with it the build is cached again |
| `go build`, module download | `writing stat cache: mkdir ~/go/pkg/mod/cache/download/…: permission denied`, non-fatal for a module with no dependency | write on `~/go/pkg/mod` when the build has to download |
| `go` telemetry | 5 to 120 refused writes to `~/.config/go/telemetry/local/*.count` per build, silent | nothing; the counters are dropped |
| `npm install` (a local dependency) | installs; two refused writes to `~/.npm/_logs/` | nothing. A registry install needs write on `~/.npm` |
| bash, interactive, the user's rc with starship | starts; no write refused | nothing |
| fish, interactive | starts; no write refused | nothing |
| zsh, interactive, oh-my-zsh with powerlevel10k and starship | starts, with three errors (below) | write on its completion dump and cache, and `TMPPREFIX` inside the private temporary directory |
| OpenCode, `initialize` and `session/new` over ACP | starts in 1.2 s and 1.8 s, as unconfined | nothing more than the device files |

### git

A worktree's `.git` is a file that points into the main copy:
`gitdir: <main>/.git/worktrees/<name>`. A commit writes the index and `HEAD` there, the
objects into `<main>/.git/objects` and the branch into `<main>/.git/refs/heads/`.

- With write on `worktrees/<name>` only, the commit fails at
  `insufficient permission for adding an object to repository database`.
- With `worktrees/<name>`, `objects`, and the branch's own ref and log **files**, it fails at
  `cannot lock ref 'HEAD': Unable to create '<main>/.git/refs/heads/thrA.lock'`. git creates
  the lock beside the ref, and Landlock allows creating a file through a rule on its
  directory only.
- With `worktrees/<name>`, `objects`, `refs` and `logs`, the commit succeeds. One write is
  still refused, `packed-refs.lock`, and git goes on without it.
- With that list the agent could not write `<main>/.git/hooks/pre-commit`,
  `<main>/.git/config` or a file of the main copy's working tree. All three were refused.

Write on the whole of `<main>/.git` also works and is the list to avoid: a hook planted there
runs as the user at their next commit in the main copy.

What the narrow list still gives away: `refs` is every branch's. A confined agent can move
`refs/heads/main` or a sibling thread's branch. Umbral names a thread's branch
`umbral/<thread>` (`worktrees/domain.BranchFor`), so every thread's ref and its lock live in
one directory, `refs/heads/umbral/`. Giving each thread a directory of its own, a branch such
as `umbral/<thread>/work`, would let the rule name `refs/heads/umbral/<thread>/` and nothing
else. That is a change to the worktree engine and not measured here.

### zsh under oh-my-zsh

zsh came up and ran its command, with these on the terminal:

- `mv: cannot stat '~/.zcompdump-<host>-5.9.<host>.<pid>'`: `compinit` writes its dump beside
  the old one in `$HOME` and renames it.
- `process substitution failed: permission denied`: zsh makes its temporary files under
  `/tmp`, from `TMPPREFIX`, and does not read `TMPDIR`.
- `free(): invalid pointer`, once, from a child process during start-up. Not chased.

Refused writes: `~/.zcompdump*` (three names), `~/.oh-my-zsh/cache/completions/`, and
`/tmp/zsh*`. Neither starship nor powerlevel10k was refused anything.

## The read list

Landlock has no deny rule. "Everything the user can read, minus two directories" is built by
allowing every sibling along each denied path's ancestors.

- The bench's profile came to **146 path rules**.
- Applying it cost about **4 ms** per command (50 confined runs of `true` in 0.195 s).
- The sibling worktree's file, the database file and the token were refused. So was listing
  `worktrees/`.
- **A file created after the sandbox starts, directly in an ancestor of a denied directory, is
  not readable.** A file created 1 s into the run in the bench's root was refused; one created
  in an allowed directory was read. For Umbral the ancestors are `$HOME`, `~/.local`,
  `~/.local/share` and `/run/user/<uid>`. A tool that replaces a dotfile in `$HOME` by
  renaming a new one over it makes a new file, which a running sandbox does not see.
- **The ancestors themselves cannot be listed.** `ls` of the bench's root failed. Granting
  directory listing on an ancestor fixes that and also lists the denied directories beneath
  it, since a rule on a directory covers its whole hierarchy: with it, `ls` of the sibling
  worktree printed its file names, and reading the file was still refused.

## The network

Landlock's network rules cover TCP `bind` and `connect`, by port. UDP is not covered at ABI 8.

- **With every TCP port denied, OpenCode does not start**: `ServeError`. `opencode acp` binds
  `127.0.0.1:4096` and connects to it; the ACP side talks to its own HTTP server.
- **With bind and connect allowed on 4096 only**, it starts and answers `session/new`.
- **With 4096 and Ollama's 11434**, a prompt completed (`stopReason: end_turn`, 6343 input
  and 56 output tokens, `gpt-oss:20b`). During it OpenCode tried 48 connections to port 443
  of `104.16.*.34`, npm's registry, all refused, and went on. The provider package was
  already in the agent's home from an earlier unconfined start; a first start with an empty
  home and no network was not measured.
- Its DNS query went out over UDP to `127.0.0.53:53`, which no rule could stop.

## What this fixes for T-HRD-15 (S3)

1. **Always in the write list**, beyond decision 5's three entries: the device files
   `/dev/null`, `/dev/zero`, `/dev/full`, `/dev/tty`, `/dev/ptmx` and the directory
   `/dev/pts`.
2. **For a thread with a worktree**: the main copy's `.git/worktrees/<name>`, `.git/objects`,
   `.git/refs` and `.git/logs`. Never `.git` whole.
3. **Not in the default, offered through `[sandbox] write`**: `~/.cache/go-build`,
   `~/go/pkg/mod`, `~/.npm`. Without them `go build` and a local `npm install` still work,
   slower and with warnings.
4. **zsh**: set `TMPPREFIX` under the private temporary directory in a confined shell. The
   completion dump and oh-my-zsh's cache stay refused by default; the shell runs without them.
5. **The read list** is generated at launch from the two denied directories. No listing right
   on the ancestors: it would show sibling worktrees' file names.
6. **The profile is built per launch**, because it names the thread's own worktree and home.

## What this sizes for T-HRD-20 (S1)

- "No network" is not what Landlock gives. It can deny TCP and cannot deny UDP, so a confined
  agent can still send datagrams. The disclosure has to say TCP.
- OpenCode needs one loopback port for itself, 4096. The profile must allow bind and connect
  on it. Whether two hosted threads at once need two ports was not measured.
- The agent reaches its model through an HTTP base URL, so the relay is an HTTP listener and
  not a Unix socket: a loopback TCP port the profile allows, served by the daemon, forwarding
  to the provider through the gateway that already writes `egress_log`. A port rule allows
  that port on every host, not only loopback, which the disclosure must say too.
- The relay is small: one listener per hosted thread and one forwarding handler. The work is
  in giving the agent the relay's URL instead of the provider's, and in the first start's
  package download, which a no-TCP profile refuses.

## Added after T-HRD-15's review: local sockets

Measured on the same machine, with the base profile and `/dev/null`:

- `busctl --user list` listed the user's bus from inside the sandbox. Landlock's file rules do
  not mediate a connection to a Unix-domain socket.
- `systemd-run --user --quiet --wait --pipe touch <a path outside the profile>` created the
  file. A process confined by Landlock alone runs anything it likes through the user's
  service manager.
- With a seccomp filter that fails `socket(AF_UNIX)` with `EACCES` and `io_uring_setup` with
  `ENOSYS`, both commands failed ("Failed to connect to user scope bus"), no file was made,
  `getent hosts localhost` and `git status` still worked, and OpenCode completed a turn
  (`end_turn`, 6343 input tokens) with TCP limited to its own port and the model's.

So the write list of the first section is not what confines a hosted agent on its own; the
filter is part of the sandbox (delta `2026-11-sandbox-linux`, decision 13). `/dev/ptmx` and
`/dev/pts` left the default write list for the same review: OpenCode, a commit and a build ran
without them.

## Not measured

- A first OpenCode start with an empty home and TCP denied.
- Two hosted agents at once.
- Whether a confined process can connect to the daemon's Unix socket by path. The token it
  would need was refused, so it could not authenticate.
- A kernel without Landlock, and one older than 6.7, which has no TCP rules.
- macOS (T-HRD-16).

## For the implementation

`golang.org/x/sys` v0.48.0, already a dependency, has the syscall numbers, the file-system
access constants and `LandlockRulesetAttr`. It has no type for a port rule; the spike passed
`struct landlock_net_port_attr` as two `uint64`. No new dependency is needed (Art. 9).
