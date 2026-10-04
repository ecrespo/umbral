# Delta — what `umb ai` sends, what it waits for, and how it exits

| Field | Value |
|---|---|
| **Status** | `PROPOSED` |
| **Date** | 2026-10-04 |
| **Task** | T-F1-19 |
| **Raised by** | T-F1-19: REQ-CLI-001, API §5.20 and Tech §5.3c, which leave these points open |

## Evidence

- **REQ-CLI-001** says `umb ai "<prompt>"` creates "an ephemeral thread in `ask` mode with stdin
  as an attachment (at most 1 MiB)" and streams the response. It does not say:
  - how the attachment crosses the wire;
  - how the 1 MiB relates to REQ-CTX-005's 256 KiB cap on every attachment;
  - what "truncation noted" (T-F1-19's What) means, or to whom;
  - what happens when nothing is piped, or stdin is a terminal.
- **API §5.20** names `attachments?:[{kind, ref | data_b64}]` and says "inline `data_b64` arrives
  with `umb ai` (T-F1-19) and is refused until then". Which kinds may carry data, and the limit,
  are not written.
- **T-F1-19's note** (delta `2026-10-wait-engine`, decision 12) makes `umb ai` the CLI's consumer
  of `thread.send`'s `wait`. It does not say which states it waits for.
- **API §5.29** says a wait observes `attention_state`, and that after `done` only a client
  viewing the thread reaches `idle`. A first implementation waited for `idle`; the real-daemon
  test hung to its deadline, because a turn nobody views ends `done`.
- **T-F1-19's What** asks for "non-zero exit code if the turn ends in error". REQ-CLI-004 gives
  the codes but not which turn endings are errors, nor what `umb` does with a turn that pauses
  for an approval it cannot answer (API §2 gives the `cli` kind no `approval.*`).

## Decisions

1. **A `stdin` attachment carries its data inline.**
   - Shape: `{kind: "stdin", data_b64, truncated?: false}`.
   - Only `stdin` carries `data_b64`, and it carries no `ref`. Every other kind carries a `ref`
     and neither `data_b64` nor `truncated`.
   - At most one `stdin` attachment per message.
   - `data_b64` is standard base64. Decoded, it is at most 1 MiB (1 048 576 bytes, inclusive).
     An empty string is accepted: it is still stdin the client chose to send.
   - Each violation is `VALIDATION_ERROR`, and nothing reaches the runtime.
   - It is open to every client kind that may call `thread.send`, not only `cli`: the wire form
     belongs to the attachment, not to `umb`.
   - *Alternative:* a separate `stdin` parameter beside `attachments`. Rejected: §4's Message
     already lists `stdin` as an attachment kind.
2. **1 MiB bounds what is sent; REQ-CTX-005 still bounds the prompt.**
   - The attachment then goes through the same 256 KiB cap as a file, cut on a rune boundary,
     with the omitted bytes counted. A NUL byte makes it binary, as for a file.
   - It is recorded in `attachments_json` as `{kind: "stdin", ref: "stdin", bytes,
     truncated_bytes}`.
   - It needs no context gatherer: the runtime builds it from the request.
3. **`truncated` means the client stopped reading, and the model is told.**
   - `umb ai` reads at most 1 MiB and one byte. The extra byte says whether more followed,
     without reading a stream that may never end.
   - Past 1 MiB it sends the first 1 MiB with `truncated: true`, and writes on stderr
     `umb ai: stdin is over 1 MiB; only its first 1 MiB is attached`.
   - The context then says
     `[the client stopped reading stdin after <n> bytes; the rest was never read]`. The bytes past
     the limit were never counted, so no number of omitted bytes can be given for them.
3a. **What is sent also fits the connection's frame limit.**
   - API §8 lets `[api] max_message_bytes` go as low as 1 MiB. 1 MiB of stdin is 1 398 104 bytes
     in base64, before the envelope. The daemon answers an oversized frame with
     `VALIDATION_ERROR` and a close (§1), after the thread already exists.
   - So `umb ai` measures the request without the data and sends the largest prefix of stdin
     whose base64 fits in the limit `system.hello` announced. It keeps 64 bytes for the envelope's
     id and marks the attachment `truncated`.
   - On stderr it says why, with the next limit to set, in the form of Tech §9.4's
     `RESULT_TOO_LARGE` message:
     ```
     umb ai: stdin is larger than the daemon's 1.0 MiB frame limit allows; only its first <n> bytes are attached.
          Raise it with: umb limits set --max-message 2MiB
     ```
   - *Alternatives:* exit 1 with the hint, which turns a usable answer into none; or raise the
     minimum frame limit to 2 MiB, which changes REQ-CLI-007's validation for one command.
4. **Nothing piped, nothing attached.**
   - A terminal on stdin is never read. Otherwise the user would have to type ^D for a prompt
     that has nothing to attach.
   - An empty pipe sends no attachment.
   - "With data on stdin" is REQ-CLI-001's trigger. Without data, `umb ai` still asks the
     question.
   - stdin is read to its end, or to the limit, before anything is sent, so the turn never
     starts on half a pipe. A pipe that never closes is waited on, as any Unix filter would wait
     on it: `ssh host umb ai …` without `-n` hangs. `--timeout` bounds the turn, not the input.
5. **The thread.**
   - `thread.create {mode: "ask", cwd: <the shell's directory>, ephemeral: true,
     title: "umb ai", model?}`.
   - `ask` exposes read-only tools only (Tech §5.3), so the model may read the repository but
     never writes, runs or fetches.
   - `--model` passes a model id. Without it the thread uses the `code` class.
   - The thread is never reused. Data Model §4 purges it after 24 h, which is T-F1-22's job.
     Until T-F1-22, what was piped stays in `messages.content` like any message (REQ-AGT-011).
     Redaction applies to what leaves the machine (Art. 4), not to the local store.
6. **The send and its wait.**
   - `thread.send` carries a ULID `client_msg_id`, as API §5.20 asks of `umb`.
   - It carries `wait: {until: ["done", "stopped", "blocked"], timeout_ms}`.
   - `idle` is **not** a target. A turn nobody views ends `done`, and a wait for `idle` would
     then run to its deadline (API §5.29). A turn that ends `idle`, because a client happened to
     view it, answers at once with that state.
   - `--timeout` sets `timeout_ms`: 10 min by default, between 1 s and 1 h (API §8).
7. **The answer streams; only the thread's own text is printed.**
   - `umb ai` prints every `thread.delta` of its thread whose `kind` is `text`, as each arrives,
     while the send's wait is still pending.
   - Draining is apart from printing. One goroutine drains the stream as fast as it delivers. It
     holds this thread's events in a queue with no bound for the printer, which waits on stdout.
     Otherwise a pager that is not reading, or a busy daemon's other threads, would fill the
     client's 1024-notification buffer. That buffer ends the stream rather than block
     (`client.ErrStreamOverflow`). The queue is bounded by the answer's own length.
   - Reasoning deltas are not printed, and neither are other threads' deltas, which every
     connection receives (API §6).
   - The output ends with a newline when the answer did not.
8. **Exit codes, within REQ-CLI-004.**
   - **0:** `thread.turn_finished` with `stop_reason = end_turn`.
   - **1, with the reason named on stderr:** any other `stop_reason`, `cancelled` included.
   - **1, and the turn is cancelled first:**
     - **The wait answers `blocked`.** `umb` cannot answer an approval, so nothing would resume
       the turn. In `ask` this needs a tool the mode does not expose, so it is a defensive case.
     - **`TIMEOUT`.**
     - **Ctrl-C.** The `thread.cancel` gets its own 3 s deadline, since the command's context is
       already over.
   - **1:** the wait answered, but no `thread.turn_finished` arrived within 5 s. The two leave
     the daemon in either order. The bus may drop an event under pressure, and an unknown end
     is not success.
   - **1:** a daemon error on `thread.create` or `thread.send`, such as `PROVIDER_UNAVAILABLE`.
   - **69:** the daemon is unavailable (REQ-CLI-003).
9. **The surface.**
   - `umb ai PROMPT… [--model M] [--timeout D]`, plus `--socket`, `--daemon-path` and
     `--no-autostart`.
   - Positionals are joined with spaces, so the prompt need not be quoted. They may come before
     or after the flags.
   - An empty prompt is exit 1 before anything is read or sent.
   - There is no `--json`. The output is the answer itself, which is what a pipe wants.
10. **The tests.**
    - `TestUmbAiPipesStdin_REQ_CLI_001` and its siblings in `cmd/umb` cover decisions 3–9
      against a fake daemon that streams notifications while the send is pending.
      - `…FitsStdinToTheFrameLimit…` covers 3a, at a 1 MiB limit.
      - `…OutlivesASlowStdout…` holds stdout while 10 000 deltas arrive.
      - `…CancelsOnInterrupt…` and `…DoesNotGuessAnUnseenEnd…` cover decision 8's Ctrl-C and its
        5 s grace.
    - `TestThreadSendTakesStdinInline_REQ_CLI_001` (`internal/api`) covers decision 1.
    - `TestAStdinAttachmentNeedsNoGatherer_REQ_CLI_001` (`internal/agents`) and
      `TestStdinAttachmentSaysTheClientStoppedReading_REQ_CLI_001` (`internal/context/domain`)
      cover decisions 2 and 3.
    - `TestUmbAiAgainstARealDaemon_REQ_CLI_001` (`cmd/umbrald`) runs the built `umb` binary
      against a real daemon and a scripted Ollama. It checks four things:
      - what is piped reaches the model;
      - the thread offers no writing tool;
      - `thread.list` does not show the thread;
      - a provider failure exits 1.
    - 28 of 29 mutations were killed. The survivor reads 100 bytes past the limit instead of 1.
      It is equivalent: the data sent is the same.
    - A live run against `gpt-oss:20b` on an isolated daemon answered a failing test's output
      and exited 0; an unknown `--model` exited 1 with `PROVIDER_UNAVAILABLE`.

## What changes in the specs on ratification

- **API:**
  - §5.20: decision 1. The inline sentence "refused until then" becomes the rule. Its Errors
    line gains the new reasons for `VALIDATION_ERROR`:
    - data on a kind other than `stdin`;
    - a `ref` on `stdin`;
    - a second `stdin`;
    - data over 1 MiB;
    - data that is not base64.
  - §4 Message: decision 2's `ref: "stdin"`.
  - §8, Limits: a `stdin` attachment is 1 MiB once decoded, one per message.
- **Tech Design:**
  - §5.3c's attachment bullet: decisions 2 and 3, replacing "`stdin` arrives with `umb ai`".
  - §9.4, the `umb` surface:
    - an `umb ai` paragraph with decisions 3a–9;
    - `umb ai` in the Commands list;
    - the sentence "Flags shared by every command: … and `--json`" becomes "… and `--json`,
      which every command but `umb ai` takes" (decision 9).
- **`specs/tasks/umbral-f1-tasks.md`:**
  - T-F1-19 is marked done.
  - Its Files: `cmd/umb/ai.go`, `internal/api/threads.go`, `internal/agents/runtime.go`,
    `internal/context/domain/attachment.go`.
  - The matrix: REQ-CLI-001 adds the tests of decision 10.
