# Delta — what "a client focuses the thread" means

| Field | Value |
|---|---|
| **Status** | `PROPOSED` |
| **Date** | 2026-10-17 |
| **Task** | T-F1-24 (REQ-AGT-016, REQ-AGT-017) |
| **Raised by** | REQ-AGT-016 moves `done` to `idle` "when a client focuses the thread", and API §7 has a turn end `idle` "while being viewed". No method, parameter or rule said how a client focuses or views a thread |

## Evidence

- **REQ-AGT-016** names the trigger, a client focusing the thread, and nothing more.
- **API §7**'s attention diagram has `done --> idle: a client focuses it` and
  `working --> idle: turn finished while being viewed`. No section defines either.
- **API §5.3** types `focused.thread_id`, and its `threads` member was always `[]`. Nothing wrote
  either one.
- **API §6** lists `thread.attention_changed`, and nothing emitted it.
- **API §5.29** says `done` stays until a client views the thread "(REQ-AGT-016, T-F1-24)". Until
  this task nothing could ever move it, so a wait for `idle` alone ran to its deadline.
- **Data Model §2.5** gives `seen_at` the meaning "when a client last focused it". No code wrote
  it.

## Decisions

1. **Focusing is an explicit method, `thread.focus {thread_id: string | null}` → `{}`.** It is
   written under API §5.23, beside `thread.get`, because the schema check reads only numbered
   `### 5.N` headings.
   - **Reading is not viewing.** `thread.get`, `thread.list` and `session.snapshot` never clear
     `done`. A script polling a thread would otherwise mark as seen a turn no person has looked
     at, and §5.29's wait for `done` would race its own reads.
   - **`thread_id` is required, and `null` is allowed.** A missing member is `VALIDATION_ERROR`,
     so a client that forgot it is told, not taken as unfocusing.
   - **An unknown thread** is `NOT_FOUND`, and it leaves the connection's view as it was.
2. **Viewing is per connection.**
   - A connection views one thread or none, and a later `focus` replaces its view.
   - The view ends when the connection closes. A crashed TUI therefore stops viewing.
   - A thread is **viewed** while at least one connection views it.
   - Views live in the daemon's memory. A restart starts with none: every connection died with
     the old daemon.
3. **What a view does.**
   - **Focusing a `done` thread** moves it to `idle`, writes `seen_at`, and announces
     `thread.attention_changed`.
   - **Focusing a thread in any other state** (`idle`, `working`, `blocked`) only writes
     `seen_at`. Nothing is announced, because nothing changed.
   - **A turn that ends while its thread is viewed** ends `idle` and writes `seen_at` with its
     end. That is §7's "turn finished while being viewed".
   - **Neither moves `updated_at`.** Viewing a thread does not reorder `thread.list`.
4. **`thread.attention_changed` follows every change of attention written while the daemon
   runs:**
   - a turn starts: `working`;
   - it asks for an approval: `blocked`;
   - the approval is answered: `working`;
   - the turn ends: `done`, or `idle` when the thread is viewed or the turn was cancelled;
   - a `done` thread is focused: `idle`.

   Data Model §6 also changes attention at start, before any client connects. Those changes
   are not announced: `session.snapshot` carries them.
   - **The answered approval is announced by the resuming turn,** not by `approval.respond`. A
     turn that ran on to its end at once could otherwise have its `working` arrive after its
     `idle`.
   - **One write is not announced.** When an approval is answered and the turn is cancelled
     at once, the store goes `blocked → working → idle`, and only the `idle` is announced: the
     cancel wins, and the turn never resumes to announce `working`.
5. **One order per thread.**
   - Each change is announced after its write, in the order written, under the thread's own
     attention lock in the runtime (DD-007). The answered approval's `working` is the one
     change written outside the lock, by `approval.respond`. A focus cannot be misled by it,
     because a focus acts only on `done`.
   - The locks are never dropped. A thread costs one mutex for the life of the daemon,
     ephemeral `umb ai` threads and purged ones included. This is a few dozen bytes each.
   - The same lock covers a focus and a turn's check for viewers before its end is written. A
     focus can therefore never land between that check and the write, which would leave a
     viewed thread `done`. `TestAFocusRacingATurnsEndLeavesNoDone` forces exactly that
     interleaving.
6. **`session.snapshot` carries the threads.**
   - `threads` is what `thread.list` returns: the non-ephemeral threads, each with its
     `attention_state`. This is the base a client applies `thread.attention_changed` to.
   - `focused.thread_id` is the thread focused most recently by a connection that still views
     one, and null when none does. It is not persisted, by decision 2. It can name an ephemeral
     thread, which `threads` leaves out.
   - **The snapshot now depends on the agent store.** A failing `thread.list` fails the whole
     `session.snapshot`. Before, only the workspace tree could fail it. A partial bootstrap
     would leave a client's cache wrong without a word, so this is deliberate.
7. **Who may focus.** Interactive clients only (§2). `umb` drives threads and views none: the
   `cli` row does not change, and `umb` gets `METHOD_NOT_FOUND`.
8. **`done` survives a restart.**
   - Data Model §6 step 3 is unchanged: it stops the turns left `running` or
     `awaiting_approval`, with `idle`.
   - A thread that ended `done` and was never viewed is still `done` after a restart, because
     REQ-AGT-016 holds until a client focuses it.
9. **The TUI's agent panel views its thread while the panel is on screen.** It focuses the
   thread once the thread exists and again whenever the panel reopens. It focuses none when the
   panel closes.
   - Leaving agent mode keeps the panel on screen, and so keeps the view.
   - **The calls are sent in the order the panel makes them.** Bubble Tea runs commands
     concurrently, so a focus made before an unfocus could reach the daemon after it. The TUI
     sends them one at a time, and a call that finds a newer one already sent sends nothing.
   - **Failures are not reported.** A failed focus leaves the thread `done`, which is what an
     unviewed thread shows anyway. A failed unfocus leaves the view in place until the panel's
     next call or until the TUI disconnects.
   - **A connection that called `thread.focus` is unfocused when it closes, even if the call
     failed.** The runtime records the view before it marks the thread seen, so a failed call
     can still leave one behind.
10. **Waits hear the change.**
    - `thread.wait` subscribes to `thread.attention_changed`, so a wait for `idle` on a `done`
      thread settles when the thread is focused. It no longer waits for the backstop's next
      read.
    - §5.29's sentence now names `thread.focus`.
11. **REQ-AGT-017 is tested on a clean shutdown.** `TestCrashRecovery_REQ_AGT_011` (T-F1-22)
    already covers SIGKILL. `TestThreadsResumeAfterRestart_REQ_AGT_017` stops the daemon with
    SIGINT while one thread's model is streaming and another thread's turn is unviewed, then
    restarts it on the same database. It checks:
    - every thread is back with its history;
    - the interrupted thread is `stopped/idle`, the unviewed one `idle/done`, and the untouched
      one `idle/idle`;
    - the stopped thread resumes with its whole history;
    - focusing the `done` thread announces `idle`.

## What changes in the specifications

- **API §5.3:** what `focused.thread_id` and `threads` hold (decision 6).
- **API §5.23:** the heading gains `focus`, plus decisions 1–3 and 7.
- **API §5.29:** "views the thread" names `thread.focus`, and a viewed turn ends `idle`
  (decision 10).
- **API §6:** the `thread.attention_changed` row says when it is sent (decision 4).
- **API §7:** the attention diagram gains `done → working` (a turn starts on a thread nobody
  viewed) and the two cancel edges to `idle`.
- **Data Model §2.5:** `seen_at` is also written by a turn that ends while its thread is viewed
  (decision 3). This is folded at ratification.
- **Tech Design §5.1:** the agent runtime keeps the views and the per-thread attention lock
  (decisions 2 and 5). This is folded at ratification.
- **PRD:** no change. REQ-AGT-016 and REQ-AGT-017 are met as written.

These API sections are edited on the task branch, because `task schema` makes a served method
absent from §5 a blocking difference. The rest is folded at ratification, as for every F1
delta.

## Tests

| Test | Proves |
|---|---|
| `TestDoneUntilFocused_REQ_AGT_016` (`internal/agents`) | `done` until focused; a read is not a focus; a viewed turn ends `idle`; two viewers; a refused focus keeps the view; every change announced after its write |
| `TestAFocusOnAThreadNotDoneChangesNothing` | a focus on `idle`/`blocked` announces nothing; the `working`/`blocked`/`idle` sequence |
| `TestAFocusRacingATurnsEndLeavesNoDone` | decision 5; it reddens when the focus does not take the lock. It forces the interleaving with a sleep, so on a starved machine it can pass without exercising the race |
| `TestAFailedFocusIsStillForgottenOnClose_REQ_AGT_016` (`internal/api`) | decision 9's last point; it reddens when the connection is marked viewing only after a successful call |
| `TestFocusCallsKeepTheirOrder_REQ_AGT_016` (`internal/tui`) | decision 9's ordering; it failed before the TUI ordered its calls |
| `TestSeenAndViewedEnds_REQ_AGT_016` (`threadstore`) | `seen_at` and the attention written by `FinishTurn` and `MarkSeen` |
| `TestThreadFocusReachesTheWire_REQ_AGT_016`, `TestSnapshotCarriesTheThreads_REQ_AGT_016` (`internal/api`) | the method, its validation, the `cli` refusal, the view ending with its connection, the notification's shape, the snapshot |
| `TestAFocusSettlesAWaitForIdle_REQ_AGT_016` (`internal/waits`) | decision 10, with the backstop set to an hour |
| `TestThePanelViewsItsThread_REQ_AGT_016` (`internal/tui`) | decision 9 |
| `TestThreadsResumeAfterRestart_REQ_AGT_017` (`cmd/umbrald`) | decision 11, against a real daemon; it reddens when a cancelled turn is left `idle` instead of `stopped` |

## For the Tech Lead to decide

- **Decision 1:** a new method rather than treating `thread.get` as a view. Recommended, because
  scripts read threads too.
- **Decision 6:** `focused.thread_id` is not persisted, unlike the workspace and tab focus.
  Recommended: a view belongs to a live connection, and after a restart no connection exists.
