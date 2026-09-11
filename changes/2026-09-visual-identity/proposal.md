# Proposal — Umbral visual identity and application icon

> **Status:** in review
> **Affected base specs:** `specs/prd/umbral-mvp.md`, `specs/technical/umbral-architecture.md`, `specs/plans/umbral-mvp-plan.md`
> **Date:** 2026-09-11 · **Author:** Ernesto Crespo (assisted draft)
> **Input artifact:** `assets/branding/umbral-icons/` (kit v0.1)

**Problem/motivation.** The base specs do not define how the application identifies itself on
desktops. Missing:

- the icon name and the app-id;
- the `.desktop` file;
- the `.ico` / `.icns` / `.icon` formats.

Without them, the 0.1 `.deb` package and the F2 desktop client would show a generic icon. On
Wayland, GNOME also fails to associate the window with its launcher when the `app_id` does not
match the `.desktop` file name. A generated icon kit already exists (concept "door · light ·
prompt · threshold") and must be incorporated as a verifiable requirement.

**Scope.**

- What changes:
  - fixes the app-id `io.github.ecrespo.Umbral`;
  - adds packaging requirements (REQ-PKG-*);
  - adds a visual identity section to the Tech Design;
  - adds packaging tasks to the 0.1 hardening phase and to F2.
- What does **not** change: the API, the data model and the agent or terminal behavior.

**Impact.**

- No impact on the JSON-RPC contract or migrations.
- Release 0.1 (TUI only) installs icons and a `.desktop` file with `Terminal=true`.
- In F2, the Wails client uses the same app-id as `app_id`/`WM_CLASS` and the Windows and macOS formats.
- **Risk:** the default Wails v3 Taskfile regenerates `.ico` / `.icns` from a single PNG and would overwrite the macOS tile. Mitigated by REQ-PKG-006.

**Constitution check.**

- **Art. 2:** every REQ-PKG has a task and automatable verification (checksums, `desktop-file-validate`, `.ico` / `.icns` inspection).
- **Art. 9:** it enters as a Delta because it modifies specs already proposed.
- **Art. 4:** it introduces no network dependency.
- No exceptions.

**Open decision.** Alternative app-id `to.seraph.Umbral` (own domain). `io.github.ecrespo.Umbral`
is chosen because it is verified with the GitHub account, which is what Flathub asks for. Changing
it later means regenerating the kit with `tools/build.py --app-id`.
