# Delta — Declarative plugins installed by digest

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-31` by the implementing agent under the Tech Lead's standing F2 instruction; pending the Tech Lead's review |
| **Date** | 2026-10-31 |
| **Task** | T-F2-60 |
| **REQ** | REQ-PLG-001, REQ-PLG-002 |
| **Folded into** | API 1.60 (§2, §3 `plg_`, §4 `Plugin`, §5.45, §6 `plugin.changed`, §9), Tech 1.86 (§3, §5.2, §5.3's destructive list, §5.3j, §9.4), Data Model 1.28 (§2.15, §5, §6 step 10); PRD F2 0.14 (REQ-PLG-002's reading, decision 6) |

## Evidence

- REQ-PLG-001 names five kinds a manifest declares — commands, MCP servers, skills, detection
  manifests, key bindings — and asks for skills' install flow: inspect, show with a digest,
  install exactly that digest after confirmation, no plugin code run in the daemon. Nothing in
  the specs says what a manifest looks like, where a plugin lives, what a "command" is, or how a
  key binding reaches a client.
- The skill store (delta `2026-09-skills-cli`, `2026-10-skill-store`) already reads a local
  directory or `.tar.gz` with every limit and refusal a bundle needs, and its digest covers the
  files only. The MCP module adds servers with an explicit trust; detection reads TOML manifests
  at start (`2026-10-agent-detection`).
- F2f closes when "a plugin installed by digest adds an MCP server and a command" (PRD F2 §8).

## Decisions

1. **Bundle.** A plugin is a local directory or `.tar.gz`, absolute path, read by the skill
   store's bundle reader with the same limits (8 MiB, 256 entries, 1 MiB per file; no links,
   no special files). Its root holds `umbral-plugin.toml`, or a single top directory does. The
   digest is the skill store's: SHA-256 over the sorted files' paths, sizes and content hashes.
2. **Manifest.** `umbral-plugin.toml`, TOML, any unknown key refusing the bundle:

   ```toml
   name = "acme"                 # [a-z0-9]{1,32}: it prefixes nothing, but composes like MCP names
   version = "1.2.0"             # 1-64 characters
   description = "Acme tools"    # 1-1024 characters, one line shown

   [[commands]]                  # at most 32
   name = "deploy"               # [a-z0-9-]{1,32}
   description = "Deploy"        # optional
   argv = ["make", "deploy"]     # 1-64 words, the first not empty

   [[mcp_servers]]               # at most 8; mcp.server.add's fields without trust
   name = "acme"
   transport = "stdio"
   command = "acme-mcp"
   args = ["--stdio"]
   env_refs = { TOKEN = "keyring:acme/token" }

   [[skills]]                    # at most 16
   path = "skills/review"        # a directory of the bundle holding a SKILL.md

   [[detection]]                 # at most 16
   path = "detect/acme.toml"     # an agent detection manifest file of the bundle

   [[keys]]                      # at most 32
   key = "ctrl+alt+d"            # ctrl+alt+ and one of a-z or 0-9
   command = "deploy"            # one of this plugin's commands
   ```

   Every contribution is checked at inspect: each skill directory must read as a skill bundle,
   each detection file as a detection manifest, each server as `mcp.server.add` would accept it
   — through the MCP module's own `Check`, so a URL the redactor would change and a secret
   reference the backend refuses are refused too — each key's command must exist and two keys
   may not share a key. A bundle that would fail late is refused before it is shown. A name
   taken meanwhile by another client is the one refusal left to install.
3. **Inspect shows, install installs that digest.** `plugin.inspect {path}` answers the manifest,
   the files with their sizes and the digest, writing nothing and running nothing: the daemon
   parses TOML and reads files, as `skill.inspect` does. `plugin.install {path,
   expected_sha256}` reads the bundle again, refuses `CONFLICT` when its digest is not the
   expected one, and refuses `CONFLICT` before writing anything when the plugin's name, one of
   its servers' or skills' names, or one of its keys is already taken. A plugin is replaced by
   removing and installing it; there is no `replace` in this task.
4. **What install writes, in order.** The bundle is copied through a staging directory renamed
   to `plugins/<name>/` inside the row's transaction, as skills are, into `plugins/` beside the
   database with a `.umbral-plugins` marker. Then each MCP server is added and each skill is
   installed from the stored copy, through the modules' own services, so they are ordinary
   `mcp_servers` and `skills` rows. A failure there removes what this install added — not a
   server or skill another client added under the same name meanwhile — the row and the copy,
   and the install fails whole. The row is `installing` until its last contribution is in, and is
   neither listed nor runnable until then. Data Model §6 step 10, at start, undoes an
   `installing` row as remove would (decision 9), and removes a staging directory and a copy no
   row records, so a crash anywhere in an install leaves nothing half done. Step 10 runs after the MCP
   manager has started the stored servers, which is how it sees them, so an interrupted
   install's `stdio` server runs once at that start before it is removed; the user had
   confirmed it and its `env_refs`. Step 10 matches by name and trust, not by what the install
   added, which it no longer knows.
5. **MCP servers are untrusted (REQ-PLG-002).** A manifest has no `trust` key — one is an
   unknown key — and every server is added with `trust: "untrusted"`. It stays so until the user
   changes it through `mcp.server.*` (removing it and adding it trusted); the plugin never can.
   A `stdio` server starts as soon as it is installed, with its `env_refs` resolved: `keyring:`
   references, and `env:` ones only where REQ-SEC-012's degraded mode accepts them. So
   `umb plugin install` shows every reference, `NAME <- reference`, beside its server before
   asking, and the confirmation covers the secrets the plugin is given.
6. **Commands are the user's, and grant the agent nothing (REQ-PLG-002).** REQ-PLG-002 says a
   plugin's commands are "user commands subject to the policy engine". Umbral's policy engine
   decides what an agent does; a command the user launches is not decided, in a pane or through
   `pane.split`. The reading is therefore: decided whenever an agent reaches one, by every route
   it has, and not decided when the user launches one, like any user command. PRD F2 0.14 writes
   this reading beside REQ-PLG-002. A command is a named
   argv. `plugin.run {plugin, command, pane_id}` splits `pane_id` with that argv, exactly as
   `pane.split` with a `command` does, so it runs as a command the user launched; the daemon
   decides nothing for it. The agent never reaches a plugin's command through the plugin:
   commands are not offered to the model; `plugin.install`, `plugin.remove` and `plugin.run` are
   `userOnly`, refused to an agent's terminal; and `umb plugin install|remove|run` are
   destructive patterns, so an agent's `run_command` of one asks. A plugin's command line the
   agent types itself is decided as any other line.
7. **Detection manifests** are read at the daemon's start from every installed plugin's copy,
   after the built-in ones and before the local `agents/*.toml`, each layer overriding the one
   before by name, as local files already do. Like a local file, a plugin's detection applies
   from the next start.
8. **Key bindings** are `ctrl+alt+` and one of `a-z`/`0-9`, which the TUI does not use and
   shells rarely bind, so a plugin cannot take a key from the TUI or another plugin (decision 3
   refuses a key already bound). The TUI lists plugins at start and on `plugin.changed`. It
   addresses sessions, not the pane tree, so a bound key types the command, POSIX-quoted, at its
   focused pane's prompt without pressing Enter, as a restored pane's command is (REQ-TERM-011):
   the user sees the line and runs it. It types only when the pane's cursor is at a shell prompt,
   as the OSC 133 markers and the emulator say: on the alternate screen, while a command runs or
   before any marker, it types nothing and says so, since a program reading keys would take the
   letters as commands of its own. A command's words may hold no control character, so a
   manifest cannot type a newline. A bound `ctrl+alt+` key is the TUI's while the plugin is
   installed, and never reaches the program in the pane, a full-screen one with `C-M-` bindings
   (Emacs) included. The desktop does not bind them yet.
9. **Remove** deletes the plugin's MCP servers and skills by the names its manifest declares,
   only while they are still the plugin's: a server still `untrusted`, a skill still holding the
   digest the plugin installed (recorded in the manifest, `skills[].sha256`). A server the user
   made trusted by removing and adding it again, or a skill the user replaced, is the user's and
   stays. Then the row, then the copy. `plugin.changed {name, change}` announces
   `installed` and `removed`.
10. **API.** §5.45 `plugin.inspect`, `install`, `list`, `get`, `remove`, `run`, for every client
    kind, the three that change state `userOnly`; §4 `Plugin`; capability `plugins`, which §9
    reserved.
11. **CLI.** `umb plugin inspect PATH`, `install PATH [--yes]` (the manifest — every server's
    `env_refs` included — and digest shown, a terminal's `y` or `--yes`, then the digest shown
    installed), `list`, `show NAME`, `remove NAME`, and
    `run PLUGIN COMMAND [--pane ID]`, whose pane defaults to `$UMBRAL_PANE_ID`.
12. **Storage.** Migration `0010_plugins`: `plugins(id 'plg_…', name, version, description,
    source, sha256, size_bytes, manifest_json, installed_at, state)`. `manifest_json` is the
    parsed manifest, so a list does not read files; `state` is `installing` or `installed`
    (decision 4).

## Not in this task

- Replacing a plugin in place, enabling and disabling one, and signed or remote plugin sources.
- Key bindings in the desktop client.
- Reloading detection manifests without a restart.
