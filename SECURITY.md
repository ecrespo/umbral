# Security Policy

Umbral runs commands on your machine on behalf of an AI agent. We take seriously any flaw that lets
someone bypass the user's consent.

## How to report

Do not open a public issue. Use **GitHub → Security → Report a vulnerability** (Private
Vulnerability Reporting) in this repository. Include:

- a description of the problem;
- steps to reproduce it;
- the affected version or commit;
- your estimate of the impact.

Target response times:
- acknowledgement within 72 h;
- initial assessment within 7 days.

## In scope (high priority)

- Running `WriteFS`, `Exec` or `Network` tools without going through the policy engine (Constitution Art. 5).
- Bypassing secret redaction, or sending to remote providers without an `egress_log` entry.
- Accessing the `umbrald` socket without a valid token, or socket permissions other than `0600`.
- Prompt injection that manages to run destructive commands without approval.
- Sandbox or worktree escape in automatic modes (later phases).

## Out of scope

- Behavior of third-party models (Ollama, llama.cpp, LM Studio, OpenRouter…) outside Umbral's control.
- Attacks that require prior access as the same local user, unless they bypass the token or the keyring.

## Supported versions

Pre-alpha, with no release yet. Fixes land on `develop`, which is the branch to test and to
report against; `main` is updated from it when a phase closes and can be many commits behind.
From release 0.1 on, the latest tagged release is supported as well.
