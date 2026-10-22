# Delta — who holds the rule-signing key, and how the binary trusts it

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-27 — E. Crespo chose the custody decisions (an age-encrypted offline file, a sole signer with a sealed backup, the public key built into the binary, a yearly rotation with overlap) and ratified the rest with them. Folded into API 1.22, Tech Design 1.30 and Data Model 1.14` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-30 |
| **Raised by** | Analyze C-02 |

## Evidence

- **Analyze C-02:** "The release procedure that signs a rule bundle is not written anywhere: who
  holds the private key, where it lives, and how a rotation is published. REQ-SEC-014 covers the
  client side only."
- **DD-016** accepts "key management (creation, rotation, revocation) and the release process that
  signs each bundle" as its cost, and says nothing more about either.
- **The specs leave these open:** where a fresh install gets its first key; what is signed and how
  a signature names its key; what `rules.key.rotate` does; what a revocation does to the bundles a
  key verified.

## Decisions

1. **Custody** (`docs/runbooks/rule-signing.md` states the procedures).
   - The Tech Lead is the sole signer.
   - The Ed25519 private key lives only in an `age`-encrypted file, kept offline. A second copy is
     kept in another place, and the passphrase apart from both.
   - The key is never in CI or in the repository. Signing is by hand, with the key decrypted into
     a pipe and never onto the disk.
   - Rotation is yearly, with an overlap during which bundles carry both signatures (decision 6).
     Revocation is for compromise only.
   - `tools/rulesign`, the release tool, reads that key from stdin. It is outside "THE SYSTEM" the
     constitution scopes (daemon, clients, `umb`), so Art. 5's rules on the product's secrets do
     not apply to it. The runbook's rules do.
2. **Formats.**
   - **Bundle:** the exact bytes of a UTF-8 JSON document whose top-level `version` is a positive
     integer.
   - **Signature:** a detached `<bundle>.sig`, holding
     `{"signatures":[{"fingerprint":"SHA256:…","sig":"<base64>"}]}`. Each `sig` is the 64-byte
     Ed25519 signature of the bundle's exact bytes, in standard base64.
   - **Public key:** its 32 raw bytes in standard base64. That form is what `umb rules key add`
     and `rulesign verify` take.
   - **Fingerprint:** `SHA256:` followed by the unpadded standard base64 of SHA-256 over the 32
     raw bytes.
   - **Verification:** a bundle verifies when some entry names the fingerprint of a valid key in
     the trust store and its signature checks out. Only if no entry names a valid key is the
     reason `unknown_key`. When several entries verify, `verified_with` records the most recently
     added of those keys. `rulesign verify` calls the daemon's own verification function.
   - **Up to date is not a rejection.** A fetched bundle whose SHA-256 equals the active one's is
     "up to date". The request is still an `egress_log` row, but it is not a rejection under
     REQ-SEC-013: nothing is notified and nothing counts toward REQ-SEC-015's three failures.
   - **A fetch that fails is not a failed verification.** A network error or an HTTP error, such
     as a 404, is logged in `egress_log`, is retried at the next check, and does not count toward
     the three failures. Only a bundle that arrived and was rejected counts.
   - Every release attaches `rules.json` and `rules.json.sig`, re-attaching the current bundle
     when the rules did not change, so the fixed-name URL always answers.
   - **Where the signature is:** at `[update] rules_url` + `.sig`. The URL's default is the
     project's fixed-name latest release asset,
     `https://github.com/ecrespo/umbral/releases/latest/download/rules.json`. It is contacted only
     when `[update] rules_check = true` (REQ-SEC-011, Art. 4).
3. **Key identifiers follow Art. 6.** A key's id is `key_` followed by a ULID. The project's keys
   carry ids fixed in the seed, so every install shows the same id. A key added with
   `rules.key.add` gets an id minted at insertion. Signatures name keys by fingerprint, never by
   id, so a key added by hand verifies exactly as the built-in one does.
4. **The binary carries a trust seed.**
   - `internal/security/rules/trustseed` embeds the project's current public keys, a `retired`
     list and a `revoked` list, all matched against `trust_keys` by fingerprint. `retired` keys
     are never inserted; the list only keeps their `source` honest after a rotation.
   - On start, the daemon inserts each seed key whose fingerprint `trust_keys` lacks.
   - It sets `revoked_at` on each seed-revoked key whose `revoked_at` is still null.
   - The seed never re-enables remote updates. A store in `disabled_fail_closed` stays there
     until a manual `rules.key.add`, as REQ-SEC-015 says.
   - `rules.status` reports each key's `source`: `builtin` when any of the seed's three lists
     carries its fingerprint, `user` otherwise. The source is derived, not stored.
5. **Removing a key revokes it.**
   - `rules.key.remove` sets `revoked_at` and keeps the row, so a seed key the user removed does
     not come back. "Valid" in REQ-SEC-014 and REQ-SEC-015 means no `revoked_at`. This defines the
     PRD's term rather than changing it, so there is no PRD edit.
   - `rules.key.rotate` adds the new key and revokes the old one, with decision 7's consequences,
     which the confirmation states.
   - `rules.key.add` of a fingerprint on the seed's `revoked` list is `CONFLICT`, whether or not
     the store holds it.
   - Otherwise, `rules.key.add` of a fingerprint the store holds as revoked clears `revoked_at`.
     Adding one it holds as valid, for instance a key the seed inserted, changes no row. Either
     way, after the usual confirmation the change is recorded with its timestamp, and the store
     leaves `disabled_fail_closed`. That is REQ-SEC-015's manual step, and it works whether or
     not the key was already there.
   - `rules.key.rotate` is for a user's own suspected compromise. The project's scheduled
     rotation (decision 6) asks nothing of users.
6. **Planned rotation never locks a client out.**
   - The release before the switch ships the new key in the seed.
   - From then until the old key retires, bundles carry both signatures.
   - The retired key is never revoked: it stops signing, its private copies are destroyed, and it
     leaves the seed of later releases. Installed stores keep it valid, which is harmless once
     nothing it could sign exists.
   - A client can see `unknown_key` only if it is more than one rotation behind, or after a
     compromise or a lost key. Three such rejections fail it closed (REQ-SEC-015), and the
     runbook states the recovery.
7. **Revoking a key discards what it verified.** A stolen key can sign a bundle with an enormous
   version, and the monotonic check would then refuse every legitimate bundle after it. So when a
   key is revoked:
   - every `rule_bundles` row it verified is deleted, along with the memory of downgrade
     rejections (REQ-SEC-013's "SHALL NOT retry" is about the bundle refused, and the floor it
     was refused against is gone);
   - the active bundle stays active if it survives. Otherwise the newest remaining `remote`
     bundle, or `builtin`, becomes active. A `local` row is never chosen, and the override
     directory is never touched;
   - `previous_bundle` becomes the next newest remaining `remote` bundle, or none;
   - REQ-SEC-013's floor is the active bundle's version.
8. **The built-in rules are version 0.** Remote bundles start at 1. The `builtin` row is rewritten
   when a new binary carries different rules. `rules.rollback` with no `previous_bundle` answers
   `CONFLICT`. `rules.reset` always works (REQ-SEC-016).

## Impact

- **Tech 1.30:** DD-016 states decisions 2–8 and points at the runbook. §4's configuration gains
  `[update]`.
- **Data Model 1.14:** §2.4e states the seed, removal as revocation, revocation discarding bundles,
  and builtin as version 0. There is no DDL change: `revoked_at` is already in 0005.
- **API 1.22:** §3 gains `key_`. §5.34's example uses a ULID id, and `trust_keys` entries gain
  `source` and `revoked_at`. Rotate, remove, re-add and a rollback with nothing to return to are
  stated.
- **T-F1-30:** the task gains the seed, revocation that discards bundles, `tools/rulesign` mapped
  in `.go-arch-lint.yml`, and named tests.
- **Analyze C-02** closes. There is no PRD change.
