# A hosted agent behind Umbral's model relay

An external agent Umbral hosts (OpenCode, over ACP) normally reaches its model itself: Umbral
does not see that traffic, and the thread says so (`egress_visibility unobserved`). With a
relay the agent's model calls go through Umbral instead, and where the sandbox can confine its
network it reaches nothing else.

## Configuring it

Name a provider of `models.toml` in the agent's `[[acp.agents]]` entry of `config.toml`, and
point the agent's own provider at that provider's endpoint, or a path below it:

```toml
[[acp.agents]]
name = "opencode"
manifest = "opencode"
command = "/home/me/.opencode/bin/opencode"
args = ["acp"]
relay = "ollama"            # a [[providers]] id of models.toml
[acp.agents.config]
model = "ollama/gpt-oss:20b"
[acp.agents.config.provider.ollama]
npm = "@ai-sdk/openai-compatible"
[acp.agents.config.provider.ollama.options]
baseURL = "http://127.0.0.1:11434/v1"   # the provider's base_url, or below it
```

- The entry takes no `env_refs`: the provider's key stays with Umbral, which adds it to each
  request. An entry that names both stops the daemon at start.
- An endpoint that is not the provider's (another host, port or scheme, a path above it) is
  refused when the thread is created, with `reason: relay`.

## What the thread says

- `egress_visibility confined`: the sandbox enforces a network. The agent's model calls go
  through Umbral, and each that leaves the machine is an `egress_log` row (forwarded as the
  agent wrote it, without redaction). It reaches nothing else over the network but its
  relay's port and its own port: on Linux it can connect to those two ports on any host, and
  on macOS its own port can be reached from outside the machine. A terminal it opens unasked
  in `full-auto` has no network; a command you approve runs with yours. Umbral still does not
  record its file reads.
- `egress_visibility unobserved`: the sandbox is off or cannot confine a network. The agent
  still uses the relay for its model, and those calls are still recorded, but nothing keeps
  it from the rest of the network.
