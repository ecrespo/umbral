# Delta — umbrald under a service manager, and the release archives

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing instruction; pending the Tech Lead's review |
| **Date** | 2026-10-31 |
| **Tasks** | T-PKG-06, T-PKG-07 |
| **REQ** | REQ-HND-001, REQ-HND-002 (the handoff under a unit); REQ-PKG-001, REQ-PKG-002 (the Linux archive's `share/`) |
| **Folded into** | Tech 1.90 (DD-014, §9.3) and 1.91 (§9.3) |

## Evidence

- Delta `2026-10-deb-package` created T-PKG-06 and T-PKG-07 and decided neither the unit's or the
  agent's policies nor the archives' layout.
- Spec-guardian's review of both tasks: with `KillMode=control-group` and `Type=simple`, a live
  handoff (`umbrald --takeover`, DD-014) under the unit ends the unit when the old daemon exits,
  and the stop kills every shell the new daemon adopted, which are still the unit's processes;
  a launchd agent with `KeepAlive {SuccessfulExit = false}` respawns on exit 75 or 78 every
  10 s for the whole session.

## Decisions

1. **No live handoff under systemd.** A daemon whose `SYSTEMD_EXEC_PID` is its own pid — the
   process systemd (≥ 248) executed for a unit — refuses a handoff at `hello`,
   before anything pauses, saying to run `systemctl --user restart umbrald` instead; the
   `--takeover` daemon exits 75 and the running one keeps serving (REQ-HND-002). A restart ends
   the panes' processes, as any restart without a handoff does. Handing the unit's main pid to
   the new daemon (`Type=notify`, `MAINPID=`) is left for later.
2. **The unit's stop** is `KillMode=mixed`: SIGTERM to the daemon alone, which records its
   sessions and closes their terminals, so each shell gets a closed terminal's SIGHUP; anything
   left after `TimeoutStopSec` is killed. **Restart** is `on-failure`, except for exits a retry
   does not cure: 64 (a bad flag), 65 (a database newer than the binary, or a recovery that
   fails), 75 (the database held by another runtime directory's daemon) and 78 (a settings or
   models file that cannot be read). An exit 0 because an autostarted daemon already serves the
   installation ends the unit.
3. **The service's environment is the manager's.** A daemon the user manager or launchd starts
   gives its panes that environment, not a terminal's: `PATH` without the user's additions,
   possibly no `LANG`, and none of the variables the env fallback of REQ-SEC-012 would read.
   Users set what they need in `~/.config/environment.d/*.conf` (systemd) or with
   `launchctl setenv` (macOS); panes' shells still read their own rc files.
4. **The launchd agent** runs `/usr/local/bin/umbrald` at load (`RunAtLoad`) and is never
   restarted by launchd (`KeepAlive` false): `umb` autostarts the daemon when it is not
   running, which covers a crash without a respawn loop on an exit no retry cures, or on a
   binary Gatekeeper kills. `ProcessType` is `Interactive`. Under launchd a live handoff works:
   the old daemon's exit 0 is not restarted, and every pane's shell leads its own session, so
   launchd ending the job's process group does not reach it; the new daemon then runs outside
   launchd until the next login. The archive never loads the agent.
5. **The archives.** `umbral-<version>-<os>-<arch>.tar.gz`, `<version>` being `git describe`
   without its `v`, holds one directory of that name: `bin/` with the three binaries, `LICENSE`
   and a `README`; Linux adds the kit's `share/` (`applications/`, `icons/hicolor/`) as it is,
   macOS `share/launchd/` with the agent. Written without owner names, mtimes from the commit,
   and on macOS without AppleDouble or extended-attribute entries. macOS archives are unsigned
   in 0.1; the README says how to clear the quarantine attribute.
