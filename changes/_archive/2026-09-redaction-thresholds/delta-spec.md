# Delta — what REQ-SEC-001's entropy thresholds mean, and what they cannot catch

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-27 — approved by E. Crespo together with the other four F1 deltas of T-F1-02…T-F1-10, as written. Folded into PRD 1.14, Tech Design 1.24 and Data Model 1.11` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-04 |
| **Raised by** | The `spec-guardian` review of T-F1-04, 2026-09-27 (finding 2) |

## Evidence

REQ-SEC-001: "a generic detector that only fires on strings with entropy ≥ 4.5 bits per
character and length ≥ 20".

1. **The two numbers are inconsistent.** Entropy measured over a string's own characters is at
   most log2(n) bits, so 4.5 bits needs 23 characters (log2 22 ≈ 4.46). The 20-character floor
   never has an effect.
2. **The REQ sets no recall.** Measured on random keys, the 4.5-bit floor alone passes ~98 % at
   40 characters, ~70 % at 32 and ~5 % at 24. A short key of a format no named rule knows
   passes through.
3. **Things above both thresholds are not secrets.** Umbral's own ids (`thr_` + ULID) reach
   4.7 bits and long Go test names 4.56; redacting them would break the agent's addressing and
   its reading of test output.

## Decisions

1. **The thresholds are necessary, not sufficient** ("only fires on"). Above them, two classes
   are left alone: a type-prefixed ULID (`^[a-z]{2,5}_` + 26 Crockford characters), and a token
   built like an identifier — split at `_ - + / =`, case changes and letter–digit boundaries, no
   more than a tenth of its characters in pieces shorter than three. Measured cost: ~0.05 % of
   bare random keys; ~6 % of 40-character keys glued to a long identifier-like prefix.
2. **Recall is stated, not raised by stealth.** The PRD text is amended to drop the dead 20-char
   floor or restate it, and to say that the generic detector is a backstop whose recall depends on
   length (the numbers above); known formats are the named rules' job. Raising recall (a
   length-scaled threshold, scoring a random-looking suffix separately) is a separate decision.
3. **Known gaps, recorded:** the candidate characters exclude `.`, so a key containing a dot is
   split and may pass; a lower-case or YAML `password: …` is caught by no named rule (the task
   scoped `.env`).
4. **Placeholders.** A match that is nothing but placeholders is already redacted and is left
   alone, which makes redaction idempotent; any other match is replaced whole, placeholders it
   holds included, so a literal `[REDACTED:` in the input exempts nothing.

## Specification changes

- **PRD REQ-SEC-001:** on ratification, the threshold sentence as decision 2 words it.
- **Tech Design 1.17 DD-008:** written by T-F1-04; points here.

## Verification

`go test ./internal/security/domain/ -run 'REQ_SEC_001|Entropy|RandomKeys|UmbralIDs|WordLike|Idempotent'`.
