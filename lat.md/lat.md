This directory defines the high-level concepts, business logic, and architecture of this project using markdown. It is managed by [lat.md](https://www.npmjs.com/package/lat.md) — a tool that anchors source code to these definitions. Install the `lat` command with `npm i -g lat.md` and run `lat --help`.

- [[architecture]] — why the daemon owns state, how module boundaries are enforced, the bus, the wire format and persist-before-notify.
- [[storage]] — why pragmas travel in the DSN, `threads` in migration 0001, forward-only migrations and prefixed identifiers.
