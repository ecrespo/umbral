# Configuring model providers

Umbral's agent talks to language models through *providers* you declare in one file.
This guide covers:
- writing that file;
- storing keys safely;
- staying offline;
- reading what the daemon thinks of each provider.

The reference is Tech Design §5.1. `examples/models.toml` is a complete, commented starting
point, and a test loads it exactly as shipped.

## Where the file lives

`$XDG_CONFIG_HOME/umbral/models.toml`, usually `~/.config/umbral/models.toml`, beside
`config.toml`. Without it, Umbral has no providers: the terminal works, but the agent cannot
answer.

```sh
mkdir -p ~/.config/umbral
cp examples/models.toml ~/.config/umbral/models.toml
```

**The daemon reads the file when it starts.** It can also reload providers live, through the
API's `config.reload` (§5.28), which checks the whole file before applying anything. `umb` has no
command for it yet, so from a terminal, after an edit:
1. Stop the daemon: `pkill -INT umbrald`. A clean stop closes the database properly.
2. Run any `umb` command, or open `umbral-tui`. Either one starts the daemon again.

Your workspaces come back, with a fresh shell in each pane.

## The shape

```toml
[router]
policy = "local-first"          # local-first | cost | quality
offline = false                 # true: only providers whose base_url is loopback
max_cost_usd_per_thread = 1.5   # read, not enforced yet (see Cost)

[classes]                       # fast | code | plan | embed — candidates in order
fast = ["ollama/qwen2.5-coder:1.5b"]
code = ["ollama/gpt-oss:20b", "openrouter/moonshotai/kimi-k2"]

[[providers]]
id = "ollama"                   # a-z, 0-9, _ and -; unique
type = "ollama"                 # ollama | openai-compat | lmstudio | llamacpp | openrouter
base_url = "http://127.0.0.1:11434"
[providers.options]             # tuning for the adapter, never a credential
num_ctx = 32768
keep_alive = "10m"

[[providers]]
id = "openrouter"
type = "openrouter"
base_url = "https://openrouter.ai/api/v1"
api_key = "keyring:umbral/openrouter"
```

- **A class lists candidates as `<provider id>/<model>`**, in order of preference.
  - A thread uses its class: `code` by default. The agent's compaction uses `fast`.
  - The router takes the first candidate that is up and can do what the call needs, such as
    tool calls.
  - It falls back to the next candidate on a 429, a 5xx, a transport error, an empty stream or
    a slow first token. It does not fall back once a token has reached you.
- **Model names are what the provider lists.** The daemon's catalog is API `model.list`
  (§5.26). `umb` has no command for it yet; `curl http://127.0.0.1:11434/api/tags` shows what an
  Ollama has pulled.
- **`[providers.options]`** go to the adapter. For Ollama, `num_ctx` sets the context window on
  every request (default 32768, capped at the model's own) and `keep_alive` how long the model
  stays loaded.
- **The file is strict TOML, validated against a schema.** The daemon refuses to start, and says
  why, when the file has:
  - an unknown key, type, class or policy;
  - a provider without `id` or `base_url`;
  - two providers with the same `id`.

  An absent file is fine; a broken one is not, because a guessed configuration would route
  your code somewhere you did not choose.

## Keys: never in the file

`api_key` is a **reference**, never the key itself:

| Reference | Meaning |
|---|---|
| `keyring:<service>/<account>` | An entry in the OS keyring. A path with no `/` is an account of the `umbral` service. |
| `env:<VAR>` | An environment variable. Only accepted as a fallback; see below. |

Store a key in the keyring:

```sh
# Linux (Secret Service: GNOME Keyring, KWallet)
secret-tool store --label "umbral openrouter" service umbral username openrouter

# macOS (Keychain)
security add-generic-password -s umbral -a openrouter -w
```

Then write `api_key = "keyring:umbral/openrouter"`.

**A key written in the clear refuses its own entry, not the file.** That provider is left out,
reported `down` with `plaintext_secret`, and the log names the field and what to write instead.
It never repeats the value. Every other provider still loads. An option whose name looks like a
credential (`token`, `secret`, `password`, `auth`…) is refused the same way.

**A machine without a keyring** (a container, a bare SSH session) can use the environment, if
you allow it in `config.toml`:

```toml
[secrets]
allow_env = true
```

With that, `api_key = "env:OPENROUTER_API_KEY"` works only while the keyring is unreachable,
and the provider shows `degraded` with `env_secret`. The environment is the weaker store, since
every child process inherits it. Without `allow_env`, an `env:` reference is `down` with
`env_secret_not_allowed`.

## Local, remote and offline

**A provider is local when its `base_url` is a loopback address** (`127.0.0.1`, `::1`,
`localhost`). Everything else is remote.

Every request to a remote provider is recorded **before it is sent** in `egress_log`, with:
- the host;
- the bytes;
- a SHA-256 of the payload;
- the provider;
- the thread.

The payload is redacted of anything that looks like a secret first.

**`offline = true`** discards every remote candidate.
- A remote provider is never contacted, not even to list its models.
- Its models show as `down` with `offline`.
- A class whose candidates are all remote answers `PROVIDER_UNAVAILABLE`.

Measured on 2026-10-04 (`docs/reports/us003-2026-10-04.md`): twenty agent runs, offline, with a
remote candidate listed first, sent zero requests off the machine.

**A loopback gateway counts as local.** OmniRoute, or a proxy you run yourself, has a loopback
`base_url`. What it forwards to the cloud is invisible to Umbral: `offline` does not stop it,
and its onward requests are not in `egress_log`. The example file keeps it out of every class
for that reason. Leave it out of yours whenever a call must stay on the machine.

## Presets

`examples/models.toml` carries two ready entries (REQ-LLM-007):
- **Hugging Face router**: `type = "openai-compat"`, `base_url = "https://router.huggingface.co/v1"`,
  `api_key = "keyring:umbral/huggingface"`. A `:<backend>` suffix on the model pins the inference
  provider, as in `openai/gpt-oss-120b:cerebras`.
- **OmniRoute**: `type = "openai-compat"`, `base_url = "http://127.0.0.1:20128/v1"`, no key. Set
  the port to your instance's.

## Is it working?

```sh
umb status
```

`providers:` lists each provider with its health and, when its credential is the problem, a
reason:

| Reason | What to do |
|---|---|
| `keyring_unavailable` | No keyring answered. Unlock or install one, or use `allow_env` with `env:` references. |
| `secret_not_found` | The keyring answered, but has no such entry. Check the service and account. |
| `keyring_error` | The keyring failed for another reason; the daemon's log has it. |
| `plaintext_secret` | `api_key` holds a key. Move it to the keyring and write a `keyring:` reference. |
| `env_secret` | Working, from the environment, because the keyring is unavailable (`degraded`). |
| `env_secret_not_allowed` | An `env:` reference without `[secrets] allow_env = true`, or with a keyring present. |

The catalog (`model.list`) carries each **model's** health, which is its provider's:
- It starts as `unknown` and becomes `ok` once the provider has answered.
- `down` with `discovery_failed` means the provider did not answer when its models were listed:
  check `base_url` and that the server runs.
- `down` with `offline` is a remote provider while `router.offline = true`, working as intended.

Discovery runs in the background at start, so an empty catalog in the first second can mean it
has not finished.

Ask the agent something to check the whole path:

```sh
echo "hello" | umb ai "Reply with one word."
```

It exits 0 when the model answered. On failure it exits 1, naming what went wrong: for
instance `PROVIDER_UNAVAILABLE` when no candidate of the class is up.

## Cost

Each model call is recorded in `usage` with its tokens and its cost in micro-USD. Local models
cost nothing.

`max_cost_usd_per_thread` is read and validated (at most 1e6), but **nothing enforces it yet**.
Until something does, a thread's spend on a priced remote model is bounded by the thread's
token budget and its step limit, not by this setting.
