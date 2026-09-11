# Proposal — Tell the client when its subscription is dropped

> **Status:** approved 2026-09-11, folded into API Spec v1.3
> **Affected base specs:** `specs/api/umbral-daemon-api-v1.md`
> **Date:** 2026-09-11 · **Author:** Ernesto Crespo (assisted draft)
> **Evidence:** T-F0-06 implementation

**Problem/motivation.**

API Spec §8 says that past the 8 MiB queue limit "the daemon drops the subscription and the
client re-subscribes (receiving a new snapshot)".

The client cannot do its half. §6 lists every daemon-to-client notification and none of them
says a subscription ended. A client whose subscription is dropped sees output simply stop,
which is indistinguishable from a quiet session. It would sit there showing a frozen screen
until the user noticed and did something.

**Scope.**

- What changes: one new notification, `session.unsubscribed`, added to the §6 table, and a
  sentence in §8 pointing at it.
- What does **not** change: the limit itself, the batching rule, the error table, or
  `protocol_version`, which stays at 1 because this is additive (Art. 8).

**Impact.**

- Additive. A client that ignores the notification behaves exactly as it does today.
- No client exists yet, so nothing breaks.

**Alternative considered.** Leave the client to infer the drop from silence. Rejected: there
is no timeout that distinguishes a dropped subscription from a session where nobody is
typing, and guessing wrong either freezes the display or re-subscribes constantly.

**Why the reason field.** The payload carries `reason` rather than being a bare signal, so
the same notification can carry a future case, the session being closed by another client,
say, without a second method. `slow_client` is the only value F0 emits.

**Constitution check.**

- **Art. 8:** additive within `protocol_version = 1`.
- **Art. 9:** the decision enters as a Delta instead of living in the code that needed it.
