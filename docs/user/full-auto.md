# `full-auto`, and what bounds it

A thread in `full-auto` runs its file writes and its commands without asking you, as long as
they start inside the thread's worktree. You can only switch a thread to it while it has an
active worktree, and it switches itself off when the worktree is merged or discarded.
Network tools, destructive commands and anything read from an untrusted source still ask.

## What the kernel refuses

On Linux with Landlock (kernel 6.2 or newer) and on macOS, the shell a `full-auto` thread
runs its commands in is confined by the kernel, not by Umbral reading the command line:

- it can write the thread's worktree, a temporary directory of its own, and the directories
  you list under `[sandbox] write` in `config.toml`; nothing else;
- it cannot read Umbral's own data: the database, the socket and its token, other threads'
  worktrees;
- it cannot write the repository's `.git`, so a command that commits fails. What the thread
  produces is the worktree's files, which you review and merge.

The agent panel shows `full-auto confined` on such a thread, and the desktop says so on the
thread's page.

## What it does not refuse

- **The network.** A command can send anything it can read anywhere.
- **Reading your files.** Everything your user can read outside Umbral's directories, the
  repository's main copy included.
- **Your environment.** The shell starts with the daemon's environment variables.
- The shell runs without its history file and without a framework's caches.

## When it is not confined

On Windows, on a Linux kernel without Landlock, with `[sandbox] mode = "off"`, or on any
other system where `umb status` reports the sandbox as not enforcing, `full-auto` still works and the thread reads `full-auto unconfined`: the worktree is then the only
boundary, and a command can write outside it. `umb status` says which of these applies.

A thread hosted by another agent (`umb thread create --agent`) is confined as a process, but
the terminals it opens are not, and in `full-auto` those started inside the worktree run
without asking.
