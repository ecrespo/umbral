# Delta — the release 0.1 Debian package

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-24 by the Tech Lead ("Ratifico el delta y el PR #50")`; folded into Tech 1.67 §9.3 and the hardening tasks (T-PKG-06, T-PKG-07), and archived |
| **Date** | 2026-10-24 |
| **Task** | T-PKG-03 |
| **REQ** | REQ-PKG-001, REQ-PKG-002, REQ-PKG-003, REQ-PKG-006 (no change to their text) |
| **Raised by** | spec-guardian's review of T-PKG-03: the package's layout, version scheme and verification were decided in code and stated nowhere in `specs/` |

## Evidence

Tech §9 says release 0.1 ships "Linux binaries (tar.gz + `.deb`)" and "`umbrald` as a user
service (`systemd --user` / `launchd`)". The Plan's Hardening phase lists the same, plus a
macOS tar.gz. Tech §9.3 names only `scripts/icons_check.sh` as the packaging verification. No
task in any tasks file owns the tar.gz archives, the `systemd --user` unit or the `launchd` plist.
T-PKG-03 owns only the icons and the `.desktop` file inside the `.deb`.

The repository had no `.deb` at all before T-PKG-03, so building one meant making the decisions
below.

## Decisions

1. **One package, `umbral`, under `/usr`.** It holds `umbrald`, `umb` and `umbral-tui` in
   `/usr/bin`, the eleven `hicolor` files and the `.desktop` file in `/usr/share`, and the
   licence as `/usr/share/doc/umbral/copyright`. The files are the kit's, copied and never
   regenerated (REQ-PKG-006). The Breeze variant is not installed (Tech §9.3).
2. **Built with `dpkg-deb` from binaries already built.** `packaging/deb/build.sh` takes no
   `debian/` source tree and needs no new tool: `dpkg-deb`, `objdump` and `desktop-file-utils`
   are the whole set.
3. **`Depends: libc6 (>= N)`, where N is the newest `GLIBC_` symbol version the binaries
   reference.** libghostty-vt is linked statically, so libc is the only shared library loaded.
   The build fails when it finds none. On the reference machine N is 2.36. `Recommends: git`,
   because worktrees and the git context need it, and the terminal works without them.
4. **The version comes from `git describe --tags --always --dirty`, and only a
   `vMAJOR.MINOR.PATCH` tag is a release.**
   - `v0.1.0` becomes `0.1.0`.
   - `v0.1.0-rc.1` becomes `0.1.0~rc.1`, which sorts below its release.
   - `v0.1.0-3-gabc1234` becomes `0.1.0+3.gabc1234`, which sorts above its tag.
   - An untagged build becomes `0.0.0+git.<hash>`, below every release, even when the hash
     starts with a digit.
   - `-dirty` becomes `.dirty` or `+dirty`.

   `check_selftest.sh` asserts these orderings with `dpkg --compare-versions`.
5. **Maintainer scripts call the tools; they declare no triggers.**
   - `postinst` (`configure`) and `postrm` (`remove`, `purge`, `abort-install`, `abort-upgrade`)
     run `gtk-update-icon-cache` and `update-desktop-database`.
   - Each call is guarded by `command -v` and `|| true`, so a minimal system installs without
     the tools.
   - `hicolor-icon-theme` and `desktop-file-utils` also trigger on those directories where they
     are installed. The direct calls duplicate that work, which the task's What line asked for.
6. **The package is reproducible.**
   - Modes come from `umask 022`, not from the builder's umask.
   - Mtimes are `SOURCE_DATE_EPOCH`, which defaults to the commit's time.
   - Two builds of one commit are byte-identical.
7. **The maintainer is `Ernesto Crespo <ecrespo@users.noreply.github.com>`.** The address is
   GitHub's no-reply form, so no personal email is published in the repository or in the
   package. Debian policy asks for a working address; the Tech Lead may name another one before
   0.1.
8. **Verification, in the CI `deb` job and in `task deb` / `task deb:smoke`:**
   - `packaging/deb/check.sh` reads the package with `dpkg -c` and compares each file with the
     kit. It validates the packaged `.desktop` and checks the maintainer scripts.
   - `packaging/deb/check_selftest.sh` proves that `check.sh` rejects four broken packages, and
     it checks the version orderings.
   - `packaging/deb/smoke.sh` installs the package in a clean `ubuntu:26.04` container, lets
     `umb` autostart the installed `umbrald` and read its status, and checks that the icon cache
     lists the icon before removal and not after it.
   - `task ci` does not run them. They need `dpkg-deb` and docker, which a macOS developer does
     not have, and the CI job runs them on every push.

## Spec changes on approval

- **Tech §9.3:** add a "Debian package" bullet with decisions 1 to 8 in short, and add
  `packaging/deb/check.sh`, its selftest and `smoke.sh` to "Verification".
- **Hardening tasks:** add the work Tech §9 and the Plan name that no task owns. Each one goes
  before release 0.1:
  - **T-PKG-06**, the `systemd --user` unit for `umbrald`, installed by the `.deb` and not
    enabled by default;
  - **T-PKG-07**, the Linux and macOS tar.gz archives and the `launchd` plist.

  Alternatively, the Tech Lead records that 0.1 drops them, and Tech §9 says so.

## Open

- Whether a `changelog.gz` and a DEP-5 header in `copyright` are needed for 0.1. Lintian asks for
  both, and apt does not.
- Whether `ARCH` should come from `GOARCH` once anything is cross-built. Today the host's
  architecture is used, and `ARCH` overrides it.
