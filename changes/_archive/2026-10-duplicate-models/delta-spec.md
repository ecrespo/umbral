# Delta — discovery stores one model per id

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-18` by the implementing agent under the Tech Lead's standing instruction ("si creas deltas, documentalos, apruebalos"); ratified by the Tech Lead on 2026-10-22 ("apruebo el delta", both pending deltas). Folded into Tech Design 1.53 |
| **Date** | 2026-10-18 |
| **Task** | none — a defect found by the F1 closing run, fixed on `fix/catalog-duplicate-models` |
| **Raised by** | The live US-003 run of F1's closing checkpoint |

## Evidence

The closing run of US-003 started a daemon that never found `ollama/gpt-oss:20b`. Its log
read `model discovery was incomplete … UNIQUE constraint failed: models.id`, and `models` was
empty. Ollama 0.40 lists a model pulled for two backends twice under one name in `/api/tags`,
with different digests — `gpt-oss:20b`, `gemma4:latest` and `qwen3.5:9b` on the development
machine. `Catalog.Refresh` passed both to `ModelStore.Replace`, the primary key refused the
second, and the whole replace failed, leaving the provider with no model at all. Every
daemon talking to that Ollama was affected, the developer's included. Tech §5.1 said only that
discovery "stores the result in `models`".

## Decision

1. **One row per id, the first entry kept.** A request names a model only by its id and Ollama
   picks the copy when called, so a second entry describes nothing a request can reach. The
   catalog removes repeats before storing, for every adapter, because any provider's list can
   repeat a name.
2. **The Ollama adapter keeps the same copy.** Its per-model window, which caps `num_ctx`
   (REQ-LLM-006), is taken from the first entry too, so the window the catalog shows and the
   one sent agree when the two copies report different context lengths.

## Specification changes

- **Tech Design 1.53, §5.1:** discovery stores one row per id, a provider's first entry for an
  id it lists twice, in the catalog and for `num_ctx` alike.

## Verification

| Test | What it proves |
|---|---|
| `TestAModelListedTwiceIsStoredOnce_REQ_LLM_002` | decision 1: a provider listing `gpt-oss:20b` twice refreshes without error and lists both models once, `ok`; red before the fix with the daemon's own error, the in-memory store refusing a repeated id as the primary key does |
| `TestOllamaDiscovers_REQ_LLM_002`, `TestOllamaSendsNumCtx_REQ_LLM_006` | decision 2: with a second, smaller `qwen3:4b` last in `/api/tags`, discovery keeps the first's capabilities and `num_ctx` is its 8192; both red before the adapter fix |
