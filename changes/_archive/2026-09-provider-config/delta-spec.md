# Delta — the provider configuration gets a normative shape

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-09-27 — approved by E. Crespo together with the other four F1 deltas of T-F1-02…T-F1-10, as written. Folded into PRD 1.14, Tech Design 1.24 and Data Model 1.11` |
| **Date** | 2026-09-27 |
| **Task** | T-F1-02 (implements 1–7, 9); T-F1-05 (implements 8, 8a–8c; touches API 1.18 §4 and §5.26, Tech 1.18 §5.1 and DD-008); T-F1-06 (8d, Tech 1.19 DD-005); T-F1-08 (8e, Tech 1.21 §5.1) |
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
8a. **Implemented by T-F1-05** with three more reasons a model can carry, all its provider's:
   `discovery_failed`, `no_adapter` and `invalid_config` (API 1.18 §4). T-F1-05 also adds the
   provider type `llamacpp`, which REQ-LLM-001 names and decision 2's list omitted.
8b. **Offline means no traffic.** With `router.offline = true` a remote provider is not
   contacted at all — no discovery either — and its models are listed `down` with `offline`.
   REQ-LLM-004 speaks only of routing candidates; a user who sets offline expects silence.
8c. **Discovery is egress.** Art. 4 records every request that leaves the machine, so the
   `egress_log` hook REQ-SEC-002 assigns to T-F1-07 lands with the first request that leaves:
   an HTTP transport under every adapter writes the row (thread NULL for the daemon's own
   requests) before sending, and refuses to send when it cannot. A provider never discovered —
   no keyring on the very first start — has no model rows, so "mark their models down" has
   nothing to mark until it has been discovered once; `umb status` still shows the reason.
   OpenRouter's variable price (`-1`) is stored as 0; T-F1-07's `cost` policy must not read it
   as free.
8d. **`num_ctx` when none is configured** (T-F1-06). REQ-LLM-006 sends "the model's configured
   `num_ctx`"; `[providers.options]` configures it per provider. With none configured, Ollama
   still gets one: 32768, capped at the model's own window from `/api/tags` — DD-005 exists to
   avoid Ollama's short default, so sending nothing is not an option.
8e. **What "remote" means** (T-F1-08, raised by its review). REQ-LLM-004 discards "every
   remote candidate" without defining remote. Since T-F1-05 a provider is local when its
   `base_url` is loopback, and nothing else is looked at. The consequence, written down here
   rather than left implicit: a loopback gateway that forwards to the cloud — a self-hosted
   OmniRoute, a LiteLLM proxy — is local to Umbral. `offline = true` does not stop it, and its
   onward requests never appear in `egress_log`, because Umbral's own request stops at
   127.0.0.1 (it is still redacted first, T-F1-07). The example says so beside the preset and
   keeps OmniRoute out of the default classes. **Open for the Tech Lead:** whether to add a
   per-provider `local = false` override to `models.toml` (schema, catalog and API `Model.local`
   would follow it), or to accept the gap as documented.
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
