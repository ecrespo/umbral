# Proposal — Close the Analyze findings and fold the pending deltas

> **Status:** approved and folded into `specs/` on 2026-09-20
> **Affected base specs:** constitution, PRD, API Spec, Tech Design, Data Model, plan, tasks
> **Date:** 2026-09-20 · **Author:** Ernesto Crespo
> **Evidence:** `specs/analyze/analyze-2026-09-11.md` (A-01…A-15) and `specs/analyze/analyze-2026-09-20.md` (B-01…B-10)

**Problem.** Two deltas had been waiting for approval since 2026-09-11, the CRITICAL finding A-01
kept the quality gate red, and the 1.1 expansion added ten more findings. With no code written yet,
folding everything at once is cheaper than carrying three open change sets.

**Decisions taken.**

| # | Decision | Rationale |
|---|---|---|
| B-01 | Remote rule updates require an Ed25519 signature verified against a trust store. No signature, no update | The redaction rules and the destructive-pattern list are the product's security control; TLS alone does not address a compromised endpoint |
| B-01b | Key lifecycle in the product: add, list, remove and rotate, always showing the SHA-256 fingerprint and requiring confirmation; the last key cannot be removed without `--force` | Key management is part of the feature, not an operational detail left to the user |
| B-01c | Recovery against key loss: fail closed plus `rules.rollback` to the previous verified bundle and `rules.reset` to the built-in rules, both offline and without a key | Losing the signing key must degrade the update channel, never the product |
| B-02 | Limits degrade the operation, never the connection, plus wait monitoring: inventory with age and stall flag, safe cancellation, and stall detection that notifies without killing the turn | The realistic failure is a leaked wait or a provider that stops answering, not an attack |
| A-03 | `env:<VAR>` accepted as a secret source when the keyring is unavailable, the fallback is enabled explicitly and the state is reported as degraded | Headless Linux, SSH and containers have no Secret Service. Requires the Art. 5 amendment of 2026-09-20 |
| A-08 | Reference hardware: CI runner for the gate, ThinkPad P14s Gen 6 for manual validation | Makes the NFRs falsifiable |
| A-09 | Entropy ≥ 4.5 bits/char and length ≥ 20 for the generic detector; rule precedence by depth and then by file name | Removes two decisions from the implementer |
| A-11 | `fetch_url` limited to http/https, 10 s, 2 MiB, no redirects into loopback or private ranges | SSRF through prompt injection |
| A-12 | REQ-PKG-004/005/007 move to the future F2 PRD; the MVP keeps 001/002/003/006/008 | Avoids MUSTs that cannot be closed in the MVP |
| B-05, B-06, B-07, B-10 | Folded with the recommended answers, favouring security and performance | Mechanical ambiguities |

**Also folded:** A-01 (`threads` in migration 0001), A-02 (VT appendix), A-04 (`client_msg_id`),
A-05 (`owner_thread_id`), A-06 (`env_refs`), A-07 (FTS triggers), A-13 (overlapping globs), A-15
(checksums in T-PKG-02), B-03, B-04, B-08, B-09, plus the whole visual identity delta.

**Still open after this:** A-10 (behaviour when a SQLite write fails under the persist-first rule)
and A-14, resolved by the 1.1 glossary. A-10 is tracked as an issue, not as a delta.

**Constitution check.** Two amendments to Art. 5, recorded in the constitution's amendment table:
the environment fallback and the requirement that network-delivered rule material be signed.
