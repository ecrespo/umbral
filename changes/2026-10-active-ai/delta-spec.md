# Delta — Active AI on failed blocks, local only

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-26` by the implementing agent under the Tech Lead's standing F2 instruction (extended to F2b … F2f on 2026-10-26), pending the Tech Lead's review |
| **Date** | 2026-10-26 |
| **Task** | T-F2-22 |
| **REQ** | REQ-AAI-001, REQ-AAI-002, REQ-AAI-003 |

## What the PRD and the task left open

REQ-AAI-001 asks for a one-line suggestion from a local `fast` model when a block closes with a
non-zero exit, rate-limited and published as `block.suggestion`; REQ-AAI-002 for accepting it
into a thread draft; REQ-AAI-003 for never sending block content to a remote model. Nothing says
which blocks count, what the model is given, what the notification carries, how "local" is
enforced, what happens to a failure that arrives while a suggestion is being made, or what the
TUI shows and which key accepts.

## Decisions

1. **Which blocks.** A block closed `finished` with an exit code other than 0 and 130 (an
   interrupt is the user's own doing), of origin `user`: an agent's command already has its
   agent, and a hosted or thread-owned session is the agent's. Nothing is asked while
   `[active_ai] enabled` is false, the default.
2. **What the model is given.** A fixed system prompt asking for one line — the corrected
   command or the likely fix, no explanation, no markdown — and a user message with the command,
   its exit code, its directory and the last 2 KiB of its plain output, cut at a line start. The
   output is block content: it goes through the router's redactor like every call, and only to
   a local model (decision 4). At most 64 output tokens, no tools, no reasoning.
3. **What is published.** The first non-empty line of the answer, backquotes trimmed, cut at 200
   characters, as `block.suggestion {block_id, session_id, text, model}`, broadcast like
   `block.closed`. An empty answer, an error or a timeout (10 s) publishes nothing and is logged
   at debug. Suggestions are not persisted: one is about a block the user is looking at now.
4. **Local only, enforced by the router.** The call is the `fast` class with a new
   `Call.LocalOnly`: the router drops every candidate whose provider is not local before
   anything is sent, so a class naming a remote model first still never reaches it, and a class
   with no local candidate is `ErrNoCandidate` with nothing sent (REQ-AAI-003). This is the
   same `local` as the offline filter (a loopback `base_url`, or `yzma`).
5. **Rate and concurrency.** `[active_ai] per_minute` (default 4, 1 to 60) bounds the
   suggestions asked in any sliding 60 s; a failure over it is dropped. One suggestion is made at
   a time, which keeps the local model free for the user's own turns; a failure that closes while
   one is being made is dropped too, and a dropped failure does not use up the window. The slot
   is freed before the suggestion is published. A suggestion that arrives after a newer block
   closed is not shown (decision 6), so the older answer never stands beside a newer command.
6. **Accepting (REQ-AAI-002).** The TUI keeps the newest suggestion of the focused pane's
   session, only while its block is the pane's last closed one, and shows it in the hint line with
   `alt+s accept`; `alt+s` passes to the shell when there is none, as `alt+a` does. Accepting opens
   the agent panel on a new thread of the focused workspace, created at once, with the draft
   `@block:<id> <suggestion>` in the input — the panel's own block attachment syntax — and sends
   nothing. A block that closes in the focused pane after it clears the suggestion.
7. **Where it lives.** A new leaf module `internal/activeai`, which reads `block.closed` from
   the bus and publishes `block.suggestion`; the model call and the block's output arrive
   through its own ports, which `cmd/umbrald` implements over the router and the block reader.
   It imports no other module's service.
8. **The suggestion is untrusted text.** The model read a command's output, which anyone who can
   write to the terminal shapes. The daemon removes escape sequences and control and format
   characters from it, so `text` cannot draw on a client; the TUI inserts it with every attachment
   token made inert (`@ file:…`), so the draft attaches the block and nothing the model named. The
   accepted thread is an ordinary thread: when the user sends the draft, that send is theirs and
   is routed by the thread's class like any other; REQ-AAI-003 covers what Active AI itself sends.
9. **Residuals for the Tech Lead.** "Local" is the router's own: a loopback `base_url`. A loopback
   gateway that forwards to the cloud (OmniRoute) is local here as it is for `offline`, and
   `docs/user/providers.md` says to keep it out of `fast` when Active AI is on. The block reader
   reads a block's whole plain output to keep its last 2 KiB; the stored plain output is already
   capped (`MaxOutputPlainBytes`), so the read is bounded.

## Tests

- `TestALocalOnlyCallNeverReachesARemoteProvider_REQ_AAI_003` (`internal/llmgw`).
- `TestActiveAIIsReadFromConfig_REQ_AAI_001` (`internal/config`).
- `TestAFailedBlockGetsASuggestion_REQ_AAI_001`, `TestSuggestionsAreRateLimited_REQ_AAI_001`,
  `TestNoLocalModelNoSuggestion_REQ_AAI_003` (`internal/activeai`).
- `TestTheOutputTailStartsAtALine_REQ_AAI_001` (`internal/activeai/domain`): the tail, the first
  line, and no escape sequence or control or format character in it.
- `TestActiveAIAsksTheRouterLocalOnly_REQ_AAI_003` (`cmd/umbrald`): the daemon's model call is the
  `fast` class with `LocalOnly` set and no tools.
- `TestAcceptingASuggestionOpensADraft_REQ_AAI_002` (`internal/tui`): the draft, nothing sent, a
  stale suggestion not shown, and `@file:` in a suggestion attaching nothing.
- `TestADaemonSuggestsOnAFailedBlock_REQ_AAI_001` (`cmd/umbrald`): a real daemon, `true` then a
  failing `gti status`, a scripted local Ollama asked once with the command and exit code 127,
  and `block.suggestion` on the client. That a remote candidate is never asked is the router's
  test above: a daemon test cannot run a remote provider without a remote host.

## Folded into

- PRD F2 0.12: REQ-AAI-001 and REQ-AAI-002 refined by decisions 1, 5, 6 and 8.
- API 1.55: §6 `block.suggestion`.
- Tech 1.77: §5.1 `[active_ai]`, `LocalOnly`, the `activeai` module; §5.2 its row.
- `docs/user/providers.md`: Active AI and a loopback gateway in `fast`.
- `specs/tasks/umbral-f2-tasks.md`: T-F2-22 done, its traceability rows.

## Review

spec-guardian's pass (FIX FIRST) found: the daemon's `LocalOnly` untested (a mutation removing it
passed every test; `TestActiveAIAsksTheRouterLocalOnly_REQ_AAI_003` now bites); attachment tokens
in a suggestion live in the draft (decision 8); the PRD not refined (0.12); a stale suggestion
shown beside a newer command (decisions 5 and 6); `text` not normalised by the daemon; no Tech
§5.2 row; the loopback-gateway residual (decision 9); a suggestion lost to `alt+s` while a thread
was being created; debug logs without context. A race the rate test hit 1 in 20 under `-race`
(the slot freed after the publish) was fixed with them.
