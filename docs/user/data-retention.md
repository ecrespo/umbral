# What Umbral keeps, and for how long

Everything Umbral records is in one SQLite database:
`$XDG_DATA_HOME/umbral/umbral.db`, usually `~/.local/share/umbral/umbral.db`. It includes:
- your commands and their output;
- agent threads;
- the audit of what left the machine.

Nothing is sent anywhere unless you configure a remote provider (see
[providers.md](providers.md)).

The daemon trims the database with a maintenance job (Data Model §4). It runs once when the
daemon starts and then once a day. It also optimizes the database and the search index.

| Data | Kept | Setting |
|---|---|---|
| A command's raw output (with colours and escapes) | 30 days after the command ended | `raw_output_days` |
| A command's plain-text transcript, and its full-text search entry | 180 days | `plain_output_days` |
| The command line, exit code, timing and output size | always (except the commands of a purged ephemeral thread, below) | — |
| Agent threads | indefinitely: there is no way to delete one yet | — |
| `umb ai` threads, and any thread created as ephemeral | 24 hours after their last activity | — |
| Closed workspaces, tabs and panes | 30 days after closing | `closed_structure_days` |
| The egress audit (`egress_log`) and per-call usage and cost (`usage`) | 365 days | `audit_days` |
| Metadata external tools report on a pane | until its own TTL, 24 hours at most, and never past a restart | — |

Change a window in `~/.config/umbral/config.toml`, then restart the daemon (`pkill -INT umbrald`;
the next `umb` or `umbral-tui` starts it again):

```toml
[retention]
raw_output_days = 7          # whole days, 1 to 3650
plain_output_days = 90
closed_structure_days = 30
audit_days = 365
```

A value outside 1–3650 stops the daemon and names the key. A misread setting would keep, or
delete, something you did not choose.

What a purge looks like:
- **A block past `raw_output_days`** still lists, opens and reports its size. Asking for its
  raw output returns nothing.
- **Past `plain_output_days`** its transcript goes too, and full-text search no longer finds
  the output. It still finds the command.
- **A running command, or a thread with a turn in progress,** is never touched, however old.
- **An ephemeral thread** goes with its messages and tool calls. Its usage and egress records stay
  in the audit, without the thread.
  - An `umb ai` thread is read-only and runs no command.
  - An ephemeral thread created through the API that did run commands loses their blocks with it.
    The terminal session they ran in stays.
  - While that thread's terminal is still open, the thread is kept, however old. Its terminal
    lasts until the daemon restarts.
- **A purged workspace, tab or pane number** can be given to a new one later. Numbers are unique
  among the objects that exist.

The daemon logs each run as `retention applied`, with how much each step removed.
