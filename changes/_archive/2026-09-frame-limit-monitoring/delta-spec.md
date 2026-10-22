# Delta — the frame limit in both directions: enforced, watched and adjustable from the CLI

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-26 — approved by E. Crespo together with the other three F1 deltas; the frame-limit reading "the CLI raises the limit" and the untainted catalog descriptions were chosen explicitly. Folded into PRD 1.13, Data Model 1.9, Plan 1.10 and the F1 tasks file; the API and Tech Design text is carried by each task's Spec edits and Files, and both documents say it is pending` |
| **Date** | 2026-09-26 |
| **Task** | T-F1-33, second of F1, before T-F1-01 |
| **Raised by** | The Tech Lead, 2026-09-26: "lo del límite, implementa la posibilidad de monitoreo del límite y que levante un ajuste a nivel de cli". It follows `2026-09-oversized-message`, decision 5, which left the outgoing direction open. |

## Evidence

**1. The daemon polices what it reads, not what it writes, and F0 can already break the
client.**

- **Only the client enforces the outbound limit.** Delta `2026-09-oversized-message` made the
  *inbound* limit a contract. `internal/client` refuses to read a frame over 4 MiB, but nothing
  in `internal/api` stops the daemon from writing one. A response over the limit therefore breaks
  the connection from the client's side: `umb` reports the daemon as broken, and the daemon never
  learns why.
- **F0 reaches it today.** A block's raw capture is capped at 16 MiB (Data Model §2.2–2.3).
  Base64 makes that about 21 MiB, so `umb block get --include raw` on any block over roughly
  3 MiB of raw output already fails.
- **F1 adds more ways to get there:** `thread.get` with long histories, context dumps, and MCP
  tool results passed through.

**2. Nobody can see how close anything comes.** No counter, log line or status field says how
large the largest frame was, or how many were refused. Today the first sign of trouble is a
failure.

**3. There is no knob, and nothing tells the user one is needed.**

- The limit is a constant in two places: `api.MaxMessageBytes` and `client.maxMessageBytes`.
- A user whose work needs larger answers has no option.
- A user who hits the limit gets no message saying what to change.

## Decisions

**1. The daemon never writes a frame over the limit.** Every message is measured at the encoder,
where it is serialised once anyway, before it is written. Each kind of message is handled as
follows.

- **A response over the limit** is replaced by a new error, **`RESULT_TOO_LARGE`** (`-32014`,
  the next free code in §3):
  - it carries the request's id;
  - `data.size_bytes` holds the size the result would have had and `data.limit_bytes` the limit
    of this connection, as numbers so a client can act on them without parsing a message;
  - the connection stays open, because the framing was never broken.
- **A notification over the limit** is not written. In its place the daemon writes
  **`limits.notification_dropped`** `{method, size_bytes, limit_bytes}` **under the same envelope
  `seq`**. §6's promise holds: a missing number still means "an event that was not yours", and a
  loss is announced rather than left as a gap, as `session.unsubscribed` does for output. The
  replacement is a few dozen bytes, so it cannot itself exceed the limit. `session.output` cannot
  reach the limit either, because it is batched at 32 KiB (§8). The rule exists for what F1 adds.
- **`block.get`** does not wait for the encoder. `include` selects one of `plain` or `raw`, so
  one field at most is ever shortened. It shortens `output_plain` or `output_raw_b64`
  so the whole result fits, cutting on a UTF-8 boundary for plain text and a 3-byte boundary
  before base64. It reports the cut in a **new** optional field,
  **`output_response_truncated_bytes`**: how many bytes of the stored output were left out *of
  this response*.
  - `output_truncated` keeps its meaning: the *stored* capture was cut at 16 MiB raw or 1 MiB
    plain (Data Model §2.2). Reusing it would change a field's meaning, which §9 reserves for a
    major version.
  - With the new field, a client can tell "raising the limit returns more bytes" from "those
    bytes are gone".

**2. The daemon watches the limit.** Per daemon run, it counts:

```json
"frames": {
  "limit_bytes": 4194304,        // the default for new connections
  "largest_in_bytes": 12288,     // largest frame read this run
  "largest_out_bytes": 3250000,  // largest frame written this run
  "refused_in": 0,               // inbound frames over the limit (T-F0-27's path)
  "refused_out": 2,              // RESULT_TOO_LARGE plus limits.notification_dropped
  "near_limit_out": 5            // frames written above 75 % of their connection's limit
}
```

The counters are exposed in three places:

- **`system.status`** returns them under a new `frames` object.
- **Logs.** Every refusal, and the first frame above 75 %, are logged at `warn` with the method
  and the size, never the content (Art. 7).
- **Metrics.** When OTel lands (T-F1-18), `refused_in` and `refused_out` are also exported as
  `umbral_frames_refused_total{direction}`, beside the other §7.2 metrics. Until then,
  `system.status` is the only place they appear.

**3. The limit is adjustable, and the CLI raises it through the daemon, not by editing files.**

- **The setting.** `config.toml` gains `[api] max_message_bytes`: bytes, or with a `KiB`/`MiB`
  suffix. The default is 4 MiB, the floor 1 MiB and the ceiling 64 MiB. A value outside the range
  is `CONFIG_INVALID` at start, like every other setting.
- **It is the first *live* key.** Tech §5.1 says the file is read once at start. This delta
  amends that to: *read once at start, except the keys listed as live; today that is only
  `api.max_message_bytes`*. Every other key still needs a restart. This delta does **not** bring
  a general `config.reload`; T-F1-02 still owns that.
- **Two narrow methods under a new `limits` capability.**
  - `limits.get` takes `{}` and returns `{frames: {…as above…}, configured_max_message_bytes}`.
  - `limits.set {max_message_bytes: integer}` validates the value, rewrites **that one line** of
    `config.toml` atomically, and applies the value to new connections. The atomic rewrite writes
    a temporary file in the same directory, then renames it. It adds `[api]` if the section is
    missing and keeps every other line byte for byte. Adding the section is a known edit to a
    known key, not a general TOML writer.
  - `limits.set` returns `{max_message_bytes, applies_to: "new_connections"}`.
  - **The file cases, each specified:**

    | The file | What `limits.set` does |
    |---|---|
    | absent | creates it, with `[api]` and the key and nothing else |
    | does not parse | refuses with `CONFIG_INVALID` and touches nothing |
    | the key is written as a dotted key (`api.max_message_bytes = …`) or as an inline table (`api = {…}`) | refuses with `CONFIG_INVALID` naming the line: this is not a general TOML writer, and the user edits that form by hand |
    | the key's line carries a trailing comment | the comment is kept |
    | `config.toml` is a symlink (dotfile managers) | resolves it and writes the temporary file beside the **target**, so the link survives |
  - An invalid value is `VALIDATION_ERROR`, and the file is not touched.
  - The daemon owns both the check and the write, so there is no rollback in `umb`, and a
    stopped daemon never finds a half-written file on autostart.
- **Only new connections see a change.** A live connection keeps the limit it was greeted with.
  Changing a framing limit under a reader that has already sized its buffer is how the T-F0-27
  class of bug starts.
- **Before the handshake the limit is fixed at 4 MiB.** The configured value applies only after
  `system.hello` succeeds. Otherwise 32 unauthenticated connections × 64 MiB would be about 2 GiB
  of memory an attacker could make the daemon reserve.
- **The handshake announces the limit.** The `system.hello` result gains `max_message_bytes`, the
  post-handshake limit of *this* connection. `internal/client` adopts it for its reads, capped by
  its own 64 MiB ceiling. The field is additive, so `protocol_version` stays 1. A client that
  ignores it keeps reading at 4 MiB; with a raised limit that client may refuse a large frame, and
  otherwise is no worse off than today.
- **The CLI.**
  - `umb limits` shows the limit and the counters, with `--json` as everywhere else.
  - `umb limits set --max-message <size>` calls `limits.set`.
  - `umb status` gains one line:

    ```
    frames: limit 4.0 MiB · largest in 12 KiB, out 3.1 MiB · refused 0 in, 2 out · near limit 5
    ```

  - When any `umb` command receives `RESULT_TOO_LARGE`, it exits 1 and says:

    ```
    umb: the answer is larger than the 4.0 MiB frame limit (5.3 MiB).
         Raise it with: umb limits set --max-message 8MiB
    ```

  The inbound refusal of T-F0-27 is not given a hint. It closes the connection with a null id,
  and `umb` never sends anything near the limit.

**4. The `cli` client kind gains `limits.get` and `limits.set`**, and nothing else. It does not
gain the `config.*` namespace.

## Specification changes

- **PRD** gains:
  - **REQ-API-005** · MUST · unwanted — IF a response or a notification would exceed the connection's frame limit, THEN THE SYSTEM SHALL NOT write it: a response SHALL be replaced by `RESULT_TOO_LARGE` carrying its size and the limit, with the connection kept open; a notification by `limits.notification_dropped` under the same `seq`; and `block.get` SHALL instead shorten its output to fit and report the omitted bytes in `output_response_truncated_bytes`.
  - **REQ-OBS-005** · MUST · ubiquitous — THE SYSTEM SHALL report, per daemon run, the frame limit, the largest frame read and written, the frames refused in each direction and the frames written above 75 % of their limit, through `system.status` and `limits.get`, and SHALL log every refusal with its method and size and without its content.
  - **REQ-CLI-007** · MUST · event — WHEN `umb limits set --max-message <size>` runs, THE SYSTEM SHALL validate the size, persist it as `[api] max_message_bytes` and apply it to connections opened afterwards, leaving the file untouched on an invalid value; and WHEN any `umb` command receives `RESULT_TOO_LARGE`, it SHALL exit 1 naming the size, the limit and the command that raises it.
- **API changes, section by section:**

  | Section | Change |
  |---|---|
  | §1 | The limit is the one the handshake announced: 4 MiB before it, the configured 1–64 MiB after |
  | §2 | `cli` gains `limits.get` and `limits.set` |
  | §3 | `RESULT_TOO_LARGE` `-32014`, with `data.size_bytes` and `data.limit_bytes` |
  | §4 | `Block.output_response_truncated_bytes` |
  | §5.1 | `max_message_bytes` in the hello result |
  | §5.2 | `frames` in `system.status` |
  | §5 | new `limits.get` and `limits.set` |
  | §6 | `limits.notification_dropped`, in the `limits` namespace so it does not share a prefix with the `notification.show` method |
  | §8 | the limit row |
  | §9 | the `limits` capability |
- **Tech Design:**
  - §5.1 names live keys and the `[api]` table;
  - §7.2 adds the metric;
  - §9.4 adds `umb limits` and the `status` line.
- **Versions** are assigned in the order deltas are ratified. After the handshake delta, this is
  PRD 1.14, API 1.16 and Tech 1.14.

## Verification

- `TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005`: a server with a 1 MiB limit and a
  handler returning 2 MiB. Three things are checked:
  - the reply is `RESULT_TOO_LARGE` with both sizes as numbers, and the next call works;
  - a notification over the limit arrives as `limits.notification_dropped` with its `seq`;
  - `block.get` of a large block comes back shortened, with `output_response_truncated_bytes`
    set and `output_truncated` unchanged.
- `TestFramesAreCounted_REQ_OBS_005`: every counter exercised, read back through `system.status`
  and `limits.get`, and the warn line checked to carry no payload bytes.
- `TestLimitsSetRaisesTheLimitForNewConnections_REQ_CLI_007`: against a real daemon whose every
  `XDG_*` directory, `XDG_CONFIG_HOME` included, is redirected (AGENTS.md). It checks:
  - the one line is written, and every other line and comment is kept;
  - a new connection is greeted with 8 MiB;
  - an old one keeps 4 MiB;
  - before the handshake the limit is still 4 MiB;
  - an invalid size is refused and the file's bytes are unchanged;
  - each case in the file-cases table: absent, unparseable, dotted key, trailing comment, symlink.
- `TestAnOversizedAnswerTellsTheUserHowToRaiseTheLimit_REQ_CLI_007`: a fake daemon answering
  `RESULT_TOO_LARGE` makes `umb block get` exit 1 with the hint on stderr.

## Open for the Tech Lead at ratification

1. **The reading of the request.** This draft reads "que levante un ajuste a nivel de cli" as *the
   CLI can raise the limit*: a setting, `umb limits set`, and a hint when the limit bites. The
   other reading, *the CLI only raises an alert*, keeps the monitoring and the hint and drops the
   setting and `limits.set`. That is simpler, and it leaves a user who hits the limit with no way
   out.
2. **What is bundled.** Decision 1, outbound enforcement with `RESULT_TOO_LARGE`,
   `limits.notification_dropped` and `block.get` truncation, was not literally asked for. It is here on
   purpose: the "refused out" counter has nothing to count without it, and F0 already breaks on
   large blocks. It can be split into its own delta if you prefer to ratify the monitoring alone.

## Phase

**F1, second.** `T-F1-33` follows `T-F1-32` and precedes `T-F1-01`. Every F1 method that can
answer large (`thread.get`, context, MCP results) should be born under an enforced limit rather
than retrofitted. On ratification, T-F1-01 depends on it, and the task and the plan row are
added to the F1 files.
