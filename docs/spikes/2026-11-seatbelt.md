# Spike — `sandbox-exec` on macOS for hosted agents (T-HRD-16)

Measured on 2026-11-19 on GitHub's `macos-latest` runner (macOS 26.7.2, arm64), the only Mac
this work had: a throwaway branch whose single workflow ran a shell script of probes. Nothing
was measured on a developer's Mac. The branch is deleted; the numbers below are what it
printed.

The base profile was `(version 1)(allow default)(deny file-write*)` with `file-write*` allowed
under one directory and on `/dev/null`, `/dev/zero` and `/dev/tty`, and `file-read*` denied
under another.

## Files

| Probe | Result |
|---|---|
| write inside the allowed directory | allowed |
| write outside it, in the home, by a child of the command | refused, `Operation not permitted` |
| redirect to `/dev/null` | allowed |
| read, `ls` and **`stat`** of the denied directory | refused |
| `ls` of the denied directory's parent | lists the denied directory's name |
| `chmod`, `touch -t`, `xattr -w` on a file outside | refused |
| read of a denied file through a symbolic link inside the allowed directory | refused |
| read of a denied file through a hard link outside the denied directory | **read** |
| write through a symbolic link from the allowed directory to outside | refused |
| a profile written with `/var/folders/…` instead of `/private/var/folders/…` | matches nothing: the write inside is refused |
| `mktemp`, and Apple's launchers (`/usr/bin/python3`), with the default `TMPDIR` | fail: the temporary directory is not writable |
| `bash -lc true`, `zsh -ic true` | start |
| `sandbox-exec` inside `sandbox-exec` | refused, exit 71 |
| a profile of 20 kB | accepted |

## Local sockets

A listener on a Unix-domain socket outside the allowed directory; `perl` as the client.

| Rule added | connect | socketpair | bind inside | TCP out | DNS |
|---|---|---|---|---|---|
| none | reaches | works | works | works | works |
| `(deny network-outbound (remote unix-socket))` | refused | works | works | works | **fails** |
| the same, then `(allow network-outbound (literal "/private/var/run/mDNSResponder"))` | refused | works | works | works | works |
| `(deny system-socket …)`, three spellings | reaches | works | works | works | works |
| `(deny network*)` | refused | works | refused | refused | fails |

Names resolve through a local socket, so refusing them all breaks every lookup; the resolver's
own is allowed back.

## Ways out through the system's services

Each line ran once with no sandbox, as the baseline, and once under the base profile.

| Probe | No sandbox | Base profile |
|---|---|---|
| `launchctl submit` of a job that touches a file outside | file made | refused, no file |
| `launchctl bootstrap gui/<uid>` of a plist | file made | `Bootstrap failed: 5`, no file |
| `launchctl load` of a plist | file made | `Load failed: 5`, no file |
| `launchctl asuser` | file made | the command runs confined: refused |
| `open` of a `.command` file | file made | exit 0, no file |
| `osascript`, `do shell script` | — | runs confined: refused |
| `osascript`, System Events `do shell script` | file made | refused |
| `at`, `crontab` | — | `Operation not permitted` on the tool itself |

With `(deny lsopen)` added, `open` itself fails with error -54. `(deny job-creation)` and
`(deny appleevent-send)` changed nothing these probes could see: launchd already refuses a
sandboxed caller.

## Signals

`kill -TERM` of a process outside the sandbox **succeeds** under the base profile, and still
does with `(deny signal (target others))` added. The second pass of the script, which tried
other spellings, was cut by the job's ten-minute limit before it got there.

## Not measured

- Anything on a Mac with a logged-in desktop session: the runner has a `gui/<uid>` domain but
  nobody at it.
- A real hosted agent (OpenCode) under the profile.
- Mach services in general. `(allow default)` leaves every lookup open; only the ways out
  listed above were tried.
- Intel Macs, and any macOS before 26.
