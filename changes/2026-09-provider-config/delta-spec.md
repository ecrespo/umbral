# Delta — the provider configuration gets a normative shape

| Field | Value |
|---|---|
| **Status** | `PROPOSED 2026-09-27 — pending the Tech Lead's ratification. T-F1-02 implements it and is merged with it open, at the user's instruction to carry on through T-F1-10; nothing downstream may treat it as ratified until it is.` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-02 (implements); T-F1-05 (inherits decision 8) |
| **Raised by** | The `spec-guardian` review of T-F1-02, 2026-09-27 (Art. 9: "NEEDS A DELTA") |

## Evidence

**No normative spec gave the provider configuration a shape.** Tech §5.1 named only
`config.toml`; the PRD speaks of `keyring:<path>` (REQ-SEC-004), a keyring that can be missing
(REQ-SEC-008) and an `env:` fallback behind `[secrets] allow_env` (REQ-SEC-012), but never
says which file carries providers, what an entry holds, or what `config.get` returns. The API
Spec listed `config.get`/`reload` with no result shape. The only description was the
non-normative reference architecture, `docs/ARCHITECTURE.md` §7, which T-F1-08 already
assumes (`examples/models.toml`). T-F1-02 followed it and wrote it into Tech 1.15 §5.1 and
API 1.17 §5.28; this delta is what makes that text normative instead of silent.

## Decisions

1. **`$XDG_CONFIG_HOME/umbral/models.toml`**, beside `config.toml`, holds `[router]`,
   `[classes]` and `[[providers]]`. Real TOML (go-toml/v2), validated against an embedded
   JSON Schema (santhosh-tekuri/jsonschema/v6, a new dependency). Absent file: no providers,
   not an error. `config.toml` keeps its own subset parser and gains `[secrets] allow_env`
   (default `false`).
2. **Grammar.** `router.policy` ∈ `local-first | cost | quality`; `router.offline` bool;
   `router.max_cost_usd_per_thread` a number in `[0, 1e6]`, held as micro-USD (Art. 6).
   `classes` ⊆ `fast | code | plan | embed`, each an ordered chain of `provider/model`.
   A provider: `id` matching `^[a-z0-9_-]{1,64}$` and unique; `type` ∈ `ollama | openai-compat |
   lmstudio | openrouter | yzma`; `base_url` required except for `yzma`, which requires
   `lib_path` and `models_dir`; optional `api_key`, `options`. Unknown keys are refused.
3. **`api_key`** is empty (no credential), `keyring:<service>/<account>` (no slash: service
   `umbral`), or `env:<VAR>`. A malformed reference is a malformed file. Anything else is a
   key in the clear and **refuses that entry** (REQ-SEC-004); no message ever repeats a value.
4. **`options` never carry a credential.** An option whose name contains `key`, `token`,
   `secret`, `password`, `passwd`, `auth`, `credential` or `bearer` refuses its entry the same
   way, since REQ-SEC-004's check would otherwise stop at `api_key`.
5. **Start and reload differ on purpose.** At start a refused entry is logged and reported
   `down`/`plaintext_secret`, and the other providers run (REQ-SEC-008's "start without
   aborting"). `config.reload` validates both files first and applies nothing if any entry
   is refused: `CONFIG_INVALID` with one `details` entry per entry, or one naming the file
   that does not parse.
6. **Health reasons** in `system.status`'s `providers[].reason` and `config.get`:
   `keyring_unavailable`, `env_secret` (degraded), `env_secret_not_allowed`,
   `secret_not_found`, `keyring_error`, `plaintext_secret`.
7. **The keyring is probed once per load, and only if a provider names `keyring:` or `env:`**
   — a locked desktop keyring can raise an unlock prompt that a local-only user should never
   see. `env:` is accepted only when that probe fails *and* `allow_env` is on.
8. **Models inherit their provider's state.** REQ-SEC-008's "mark their models
   `health = down` with reason `keyring_unavailable`" is closed by T-F1-05, when model rows
   exist: a model's health and reason are read from its provider, with no new column, so no
   migration 0006. `TestModelsOfADownProviderAreDown_REQ_SEC_008`.
9. **`config.get` result** is `{settings, providers, rejected}`; credentials appear only as
   their reference. `cli` is not given `config.*` (API §2 unchanged).

## Specification changes (already written by T-F1-02)

- **Tech Design 1.15** §5.1: `[secrets] allow_env`, the `models.toml` format and rules;
  §9.4: the status reason.
- **API 1.17** §5.28: `config.get`/`reload`; §5.2: `reason`; §2 and §9: capability `config`.
- **Tasks:** T-F1-05 gains REQ-SEC-008 and the test of decision 8.

No REQ text changes.

## Verification

`go test -race ./... -run 'REQ_SEC_004|REQ_SEC_008|REQ_SEC_012'`, plus
`TestMalformedModelsFileIsInvalid`, `TestAMalformedReferenceNeverEchoesItsValue` and
`TestAMalformedFileReloadNamesTheFile`.
