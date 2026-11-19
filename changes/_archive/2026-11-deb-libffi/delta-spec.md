# Delta — the daemon needs libffi, and the package does not say so

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-11-18 by the Tech Lead ("Apruebo y resuelve el 2026-11-deb-libffi con sus Q1-Q3")`, as written, after his approval of 2026-11-17; folded into Tech 1.92 §9.3 and archived. Q1 to Q3 are answered by delta `2026-11-open-questions` (L1 to L3) |
| **Date** | 2026-11-17 |
| **Tasks** | T-PKG-08 (the dependency, its check and the smoke that hid it) |
| **REQ** | none changed |
| **Folded into** | Tech 1.92 §9.3 |

## Evidence

- CI run 44206989647 (pull request #78, 2026-11-17) failed the Debian job at
  `packaging/deb/smoke_systemd.sh`: `umb: client: the daemon is not listening: no token at
  /run/user/1001/umbral/token`, exit 69. Reproduced locally three times out of three with the
  package built from the same tree.
- The unit's journal, printed by the smoke since the same day:
  `umbrald: panic: libffi.so.8: cannot open shared object file: No such file or directory`,
  from `github.com/jupiterrider/ffi.init.0()`; systemd restarts it five times and gives up.
- `github.com/jupiterrider/ffi` is a dependency of yzma (T-F2-21, 2026-10-26). Its package
  `init` loads `libffi.so.8` and panics when it is absent, so **every** `umbrald` on Linux needs
  the library at start, whether or not a `type = "yzma"` provider is configured.
- `ldd bin/umbrald` lists libc only: the library is opened at run time, so
  `packaging/deb/build.sh`, which derives `Depends` from the binaries' `GLIBC_` symbols, cannot
  see it. Tech Design §9.3 says `Depends: libc6 (>= N)` "since libghostty-vt is static", and
  `build.sh`'s header says "the only shared library the binaries load is libc". Both are false
  since T-F2-21.
- `ubuntu:26.04` does not carry `libffi8`, and `systemd systemd-sysv dbus-user-session procps`
  do not pull it in (`apt-get install -s`, 2026-11-17). `packaging/deb/smoke.sh` installs
  `libgtk-3-bin` first, which does, so the autostart smoke passes on a machine where the
  package alone would not work.
- With `libffi8` added to the container the systemd smoke passes, twice out of two.
- Why the systemd smoke was green on 2026-10-31 (T-PKG-06), five days after yzma landed, was
  not determined: the `ubuntu:26.04` image is not pinned, so what it or those packages pulled
  in that day is not known.

## Decisions

1. **The package depends on libffi:** `Depends: libc6 (>= N), libffi8`. `build.sh` writes it,
   and `check.sh` and its selftest refuse a package without it.
2. **The smokes install nothing that hides a missing dependency.** `smoke.sh` installs the
   package before the desktop tools it uses to check the icons, and starts the daemon before
   them, so `apt` resolving `Depends` is what the daemon runs on.
3. **The Linux tar.gz archive says what it needs.** It has no dependency metadata; its
   `README` names `libffi.so.8` and the `libffi8` package.
4. **Tech Design §9.3 and `build.sh`'s header are corrected** to say the daemon also opens
   libffi at run time, through yzma's `purego` bindings.

## Open questions for the Tech Lead

- **Q1 — should a daemon with no embedded provider need libffi at all?** The panic is in a
  dependency's `init`, so avoiding it means a build tag that leaves yzma out, or a change
  upstream to load the library on first use. Decision 1 is the smallest fix and does not
  preclude either.
- **Q2 — Windows.** The same `init` loads `libffi-8.dll`. CI's Windows job passes, so either the
  runner has it or the job does not start `umbrald`; this was not checked.
- **Q3 — macOS.** `libffi.8.dylib` ships with the system; not checked on a clean machine.
