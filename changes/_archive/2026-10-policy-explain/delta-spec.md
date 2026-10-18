# Delta — policy.explain: what a thread lends, what is refused, and the trace on the wire

| Field | Value |
|---|---|
| **Status** | `APPROVED 2026-10-18` by the implementing agent under the Tech Lead's standing instruction for F1; **pending the Tech Lead's review**. Folded into API 1.36, Tech Design 1.45 and the F1 tasks file |
| **Date** | 2026-10-18 |
| **Task** | T-F1-28 (REQ-SEC-009) |
| **Raised by** | API §5.36 gives the params and an example trace that predates delta `2026-09-policy-precedence`: no `exposure` step, no `decided` mark, a `mode` and a `decision` member the engine does not produce. It does not say what a `thread_id` contributes, what happens with neither a thread nor a mode, how the target is read, or who may call |

## Evidence

- **REQ-SEC-009**: the decision and "the ordered trace of the rules evaluated … naming the rule that decided".
- **API §5.36**: `{thread_id?, mode?, tool, risk, target, tainted?: false}`, a six-step example, "read-only".
- **Delta `2026-09-policy-precedence`, decision 5**: the trace is `Decision.Trace` — `exposure` plus DD-006's six, every one evaluated, one marked `decided`; "the API §5.36 example is updated here" (T-F1-28).
- **API §2**: the `cli` row does not list `policy.explain`, yet the task asks for `umb policy explain`.

## Decisions

1. **The engine is the turn's.** policy.explain calls the same `domain.Decide` with the same destructive patterns; when rule bundles replace the list (T-F1-29/30), both change together.
2. **A thread lends** its mode, the write root found from its directory with the runtime's own `isRepo`, and its rules with the global ones. A `mode` in the request overrides the thread's. Without a thread only the global rules apply and there is no write root.
3. **Refusals.** Neither `thread_id` nor `mode`, an unknown `mode` or `risk`, a `tool` empty or over 128 characters, or a `risk` that contradicts the registered tool's declared one is `VALIDATION_ERROR`; an unknown thread is `NOT_FOUND`. An unregistered tool (an MCP server not connected yet) is evaluated with the risk given. `target` may be empty.
4. **The target is shaped as a turn shapes it.** For `ReadOnly` and `WriteFS` — the file tools' risks; every MCP tool is `Network` — the target is made absolute against the thread's directory with its symlinks resolved, by the built-in tools' own function, and so are the directory and the write root. Without a thread a relative path stays as given rather than resolving against the daemon's own directory. `Exec` and `Network` targets are taken as given. `tainted` is the caller's.
5. **The wire trace** is seven `{step, matched, decided, detail?, note?}` entries in order; `reason` is absent for an allow; `allow_always` says whether an approval may be remembered as `always`. The §5.36 example is rewritten to what the daemon returns.
6. **Callers.** Every client kind, under `policy_explain`; §2's `cli` row gains `policy.explain`. `umb policy explain --tool T --risk R (--mode M | --thread ID) [--tainted] [--] TARGET…` joins its positionals into the target.
7. **Read-only** means no approval, no rule, no audit row and no tool run; the source port has no write method.
8. **Wiring.** `internal/security`'s root holds the explainer behind an `ExplainSource` port and a `ToolCatalog` port; `cmd/umbrald` adapts the agent thread store to the first, through a two-method read interface, and the tool registry with the built-ins' path function to the second. `security` imports neither `agents` nor `tools`.
9. **The destructive list must stay shared.** Both paths call `domain.Decide` with the built-in list today. When rule bundles replace it (T-F1-29/30), the runtime and the explainer must be handed the same `Policy`; the T-F1-29 task carries that note.

Raised by the spec-guardian review on 2026-10-18: the wording of 3, 4 and 8, and decision 9.
