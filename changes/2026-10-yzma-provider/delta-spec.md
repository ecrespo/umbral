# Delta — the embedded yzma provider

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-26` by the implementing agent under the Tech Lead's standing F2 instruction (extended to F2b … F2f on 2026-10-26), pending the Tech Lead's review |
| **Date** | 2026-10-26 |
| **Task** | T-F2-21 |
| **REQ** | REQ-LLM-009 |

## What the PRD and the task left open

REQ-LLM-009 asks for a provider of `type = "yzma"` that loads "a GGUF path" in process and
serves the `fast` class without a server and without network. `models.toml` already takes
`lib_path` and `models_dir` for that type (Tech §5.1), and it is listed `no_adapter` until now.
Nothing says what `lib_path` points at, which llama.cpp the build needs, how models are named,
when a model is loaded and released, what it can be asked, or what a native failure costs the
daemon.

## Decisions

1. **The binding.** `github.com/hybridgroup/yzma` v1.30.0, `pkg/llama` only, which loads
   llama.cpp's shared libraries with purego: no cgo, so the daemon's build does not change. The
   adapter never imports `pkg/download`; the libraries are the user's to install. A constitution
   amendment adds yzma to the Stack's LLM row. yzma brings `github.com/ebitengine/purego`
   v0.11.0-alpha.9, a pre-release, as its one new transitive module.
2. **`lib_path`** is the absolute path of the directory holding llama.cpp's shared libraries
   (`libllama`, `libggml`, `libggml-base` and a CPU backend), release b11480 or later, which
   yzma v1.30 needs. The development pin is b11899, installed by `task deps:llama` from
   llama.cpp's own release with its SHA-256 checked. The libraries are loaded once per process,
   on the first call, and purego cannot unload them: a provider built after that load with
   another `lib_path` is listed `down` with `invalid_config`, and a call that would load a
   second set fails permanently, naming the directory loaded. A load that failed half way
   pins its directory as well, since some of its libraries may be in the process; only that
   directory may try again.
3. **`models_dir`** is an absolute directory; each regular file or symlink in it named
   `<name>.gguf` whose first four bytes are `GGUF` is a model `<provider>/<name>`. Discovery
   reads the directory and nothing else; an unreadable directory is `discovery_failed`. A file
   whose magic is not `GGUF` is skipped and logged by name, because loading it would hand
   llama.cpp bytes it may crash on.
4. **What it serves.** Text in, text out: `local = true`, no tools, no vision, no structured
   output, no reasoning switch, a context window of `options.num_ctx` (default 4096, 512 to
   32768). Any other option is `invalid_config`. The router's capability filter (REQ-RTR-001) therefore never sends it a call with
   tools; a call it cannot carry anyway is `ErrUnsupported`. It is meant for the `fast` class,
   which the user names in `[classes]`.
5. **A call.** The model's own chat template (GGUF metadata) renders the system prompt and the
   messages, a tool message as user text. The prompt is tokenized with special tokens parsed,
   so the template's frame is read as one; a message's text naming one of the vocabulary's
   control or user-defined tokens (`<|im_start|>system`) has a zero-width space put after the
   token text's first byte first, so no message opens a turn of its own (security review of
   2026-10-26); the prompt must leave room for the answer in the
   window or the call is refused before decoding (`Permanent`). Decoding samples with
   `options.temperature` (default 0.2, 0 to 2) up to `MaxOutputTokens` (default 256, at most
   the window), streams each piece as `text_delta`, checks the context between tokens and ends
   with `usage` (prompt and generated token counts, cost 0) and `done`. One call runs at a
   time, as `max_parallel` = 1 for a local provider already queues them (REQ-ORC-005).
6. **Memory.** A model is loaded on the first call that names it and kept; a call for another
   model of the provider frees it first, so one is resident per provider. It is freed after
   `options.keep_alive` seconds without a call (default 300, 0 keeps it), and when a
   `config.reload` replaces the provider — the catalog closes a replaced provider that holds
   something, once its running call ends — so a reload never keeps two copies; the daemon's
   exit frees the rest. The daemon's idle memory without a call stays what it was.
7. **Native failures.** A crash inside llama.cpp ends the daemon — purego gives no isolation.
   What can be checked before handing it bytes is checked: the libraries' presence, the GGUF
   magic, the window, a model name with no separator and no leading dot. Running the model in a child process is the alternative, left for a
   later delta if a crash is ever seen.
8. **No network.** The adapter opens no socket and records no `egress_log` row; offline mode
   keeps it (REQ-LLM-004).

## Tests

- `TestYzmaListsTheGGUFModelsOfItsDirectory_REQ_LLM_009` (`internal/llmgw/adapters/yzma`):
  `.gguf` files with the magic are listed local with no tools; others are skipped; a missing
  directory fails discovery.
- `TestYzmaStreamsTextAndUsage_REQ_LLM_009`: against a fake engine, the template's prompt,
  whole-character `text_delta`s, `usage` and `done` (`stop`, `length`); a cancel ends the
  stream; a prompt over the window is permanent; tools, a schema, reasoning and vision are
  refused before anything loads; a name outside `models_dir` is never opened.
- `TestYzmaKeepsOneModelAndReleasesIt_REQ_LLM_009`: a second model frees the first; `keep_alive`
  frees it.
- `TestYzmaRefusesASecondLibPath_REQ_LLM_009`: a second `lib_path` after a load, a call that
  would load a second set, a failed load pinning its directory, and every configuration the
  adapter cannot run.
- `TestAReplacedProviderIsClosed_REQ_LLM_009` (`internal/llmgw`): a reload closes the provider
  it replaces and not one it keeps.
- `TestEveryProviderTypeGetsItsAdapter_REQ_LLM_001` (`cmd/umbrald`): `yzma` gets its adapter,
  and a relative path is `invalid_config`.
- `TestLiveYzmaAnswersFromAGGUF_REQ_LLM_009` (tag `live`): a message forging the template's
  control tokens renders with no more of them than a plain one (7 against 3 without the
  neutralizing); llama.cpp b11899 and the
  `qwen2.5-coder:1.5b` GGUF answered `pong` in 1.3 s, first call included. The adapter opens no
  socket, so there is no egress row to count; a router-level run waits for US-F2-002 (T-F2-23).

## Folded into

Constitution 1.5 (Stack, LLM row, and its amendment), Tech 1.75 §5.1 (the embedded provider and
the `models.toml` example), `Taskfile.yml` (`deps:llama`, `scripts/install_llama.sh`).
