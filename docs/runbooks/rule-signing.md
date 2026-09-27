# Runbook — the rule-signing key

This runbook covers the custody, signing, rotation and revocation of the Ed25519 keys that sign
Umbral's rule bundles. It closes Analyze C-02.

The mechanism is specified elsewhere:

- DD-016 in the Tech Design;
- REQ-SEC-011 and REQ-SEC-013 to REQ-SEC-016;
- `trust_keys` and `rule_bundles` in Data Model §2.4e;
- `rules.*` in API §5.34.

This document says who does what with the key. The Tech Lead made its decisions on 2026-09-27
(delta `2026-09-rule-signing-custody`).

## 1. Decisions

| Question | Answer |
|---|---|
| Who signs | The Tech Lead, E. Crespo, is the only signer. |
| Where the key lives | An `age`-encrypted file, offline. The key is never on disk in clear, never in CI, and never in the repository. |
| Backup | A second encrypted copy is kept in another physical place. The passphrase is kept apart from both copies. |
| How users get the public key | It is built into the binary as the trust store's seed. The fingerprint is also published in the README and in every release's notes. |
| Rotation | Every 12 months. During the overlap, bundles carry both signatures, so no client is locked out. |
| Revocation | Only for a compromise. A key retired on schedule is not revoked. |

**Why not sign in CI:** DD-016 signs the rules because the threat is a compromised endpoint. If
CI can use the key, then anyone who compromises the repository or the runner has the key too.
Signing by hand, once per bundle, is the cost DD-016 accepted.

## 2. What exists where

| Thing | Where | Secret? |
|---|---|---|
| Private key, encrypted | `umbral-rules-<label>.key.age` on the primary offline USB drive | Yes. It is useless without the passphrase. |
| Backup of the private key | The same file on a second drive, in another place | Yes |
| Passphrase | The Tech Lead's password manager, never beside either drive | Yes |
| Public key | `umbral-rules-<label>.pub` in `internal/security/rules/trustseed/`, the README and the release notes | No |
| Signed bundle | `rules.json` and `rules.json.sig`, attached to each GitHub release. `[update] rules_url` defaults to the latest one. | No |

The **label** (`2026a`, `2027a`, or `2026b` for an unscheduled replacement) names files and
passphrases offline. It never reaches the product. There, a key is identified by its fingerprint
and has a `key_<ULID>` id fixed in the seed (Art. 6).

## 3. Tooling

T-F1-30 writes `tools/rulesign`. The private key is read from stdin only, and the key and
signature formats are the ones DD-016 states.

| Command | What it does |
|---|---|
| `rulesign keygen` | Writes the private key to stdout. Writes the public key (base64) and its fingerprint to stderr. |
| `rulesign sign <bundle> [<bundle>.sig]` | Reads the private key on stdin and adds its signature to `<bundle>.sig`, creating the file or appending to it. |
| `rulesign verify <bundle> <pubkey-base64>` | Checks the signature with the daemon's own verification code. |

[`age`](https://age-encryption.org) encrypts with a passphrase (`age -p`). Each command below pipes
the key through memory, so the decrypted key never touches the disk.

## 4. Procedures

### 4.1 Create a key

Work on the Tech Lead's workstation with the network off.

1. `rulesign keygen 2> umbral-rules-2026a.pub | age -p -o umbral-rules-2026a.key.age`. Choose a
   new passphrase and store it in the password manager. The `.pub` file holds the public key and
   the fingerprint.
2. Copy the `.key.age` file to the primary drive and to the backup drive. Then delete the
   workstation copy with `shred -u` or an equivalent.
3. Test each drive once: decrypt from it, sign a scratch file, and check the result with
   `rulesign verify`. A backup nobody has restored from is not a backup.
4. Open a pull request that adds the `.pub` to `trustseed`, with a fixed `key_<ULID>` id. The
   description carries the fingerprint. The next release ships the key.
5. Publish the fingerprint in the README and in that release's notes.

### 4.2 Sign and publish a bundle

1. Build `rules.json`. Its `version` must be strictly greater than the last published one;
   REQ-SEC-013 rejects anything else.
2. Sign offline with every key currently signing (one key, or two during an overlap):
   `age -d umbral-rules-<label>.key.age | rulesign sign rules.json`, once per key.
3. Verify with each public key the way the binary will:
   `rulesign verify rules.json <pubkey>`.
4. Attach `rules.json` and `rules.json.sig` to the release. The default `rules_url`
   (`…/releases/latest/download/rules.json`) then serves them. **Every release attaches both
   files**, re-attaching the current pair when the rules did not change. A release without them
   turns every client's check into a 404. A 404 does not fail a client closed, but no client
   gets updates until a release has the files again (DD-016).
5. Check the result with the released binary. Set `[update] rules_check = true`, then run
   `umb rules status`: the new version should show `verified_with` set to the key's id.

### 4.3 Planned rotation, every 12 months

Old key A, new key B.

1. **Release N:** create B (§4.1). The seed carries both A and B. Bundles are signed with A only.
2. **From release N+1, for at least one release cycle and 30 days:** every bundle carries both
   signatures (§4.2 step 2), so binaries older than N (which know only A) and newer ones verify
   alike.
3. **Retire A:** bundles are signed with B only. A moves from the seed's keys to its `retired`
   list, so installed stores keep reporting it as `builtin`. Destroy
   both encrypted copies of A. **Do not revoke A.** Installed stores keep it as a valid key, which
   is harmless: nothing A could sign exists any more. Revoking it would instead discard every
   bundle it verified on clients that have fetched nothing newer (DD-016).

After step 3, a client still older than N sees `unknown_key` on each check. After three checks
it fails closed (REQ-SEC-015). It keeps its rules in force and loses nothing else. The release
notes for step 3 tell users more than one rotation behind to upgrade and then run
`umb rules key add <B's public key>`. The upgrade's seed has already added B, and the add
still works: it confirms the fingerprint and reopens updates, which the seed alone never does
(REQ-SEC-015).

Users never run `umb rules key rotate` for this. That command revokes, and it is for a user's own
suspected compromise.

### 4.4 Compromise, or suspected compromise

Do all of this the same day.

1. Create a replacement key C (§4.1).
2. Cut a release whose seed carries C and lists the compromised key as `revoked`. Upgrading
   revokes the key in every trust store: the bundles it verified are discarded, and the rules in
   force fall back to the newest bundle a valid key verified, or to the built-in rules (DD-016).
3. Sign a bundle with C alone and publish it (§4.2). Give it a version above any the compromised
   key could plausibly have signed. The revocation already resets the floor on upgraded clients,
   but a clearly higher version leaves no doubt on any client.
4. Publish a security advisory. It gives the compromised fingerprint and C's public key and
   fingerprint. It also tells users who cannot upgrade yet to run:
   - `umb rules key remove <id of the compromised key> --force`, which revokes the key and
     discards what it verified;
   - `umb rules key add <C's public key>`, confirming C's fingerprint.
5. Destroy every copy of the compromised key.

Clients that do neither see `unknown_key` and fail closed after three checks. That is the safe
outcome, because they keep their rules in force.

### 4.5 Lost key, no compromise

Both copies are lost, or the passphrase is. Follow §4.4 steps 1 and 3, but **do not revoke** the
lost key: nothing was compromised, and revoking it would discard good bundles. The next release's
seed brings the new key. The release notes carry the new key's public key and fingerprint, for
clients that fail closed in the meantime (REQ-SEC-015). Those clients keep the rules in force,
which DD-016 designed for.

## 5. Once a year

- Restore a backup (§4.1 step 3), and check that the passphrase still opens both copies.
- Check that the README, the latest release notes and `trustseed` carry the same fingerprints.
- If the signing key is 12 months old, start §4.3.
