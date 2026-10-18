# Tasks — delta `2026-10-wait-monitoring`

### [x] T-F1-31 · Wait monitoring and lifecycle
- **What:** decisions 1–9 in `internal/waits/{waits.go,domain,ports}`, `internal/agents/stall.go` and `runtime.go`, `internal/llmgw/router.go` (`Local`), `internal/integrations/rate.go`, `internal/config/settings.go` (`[waits]`), `internal/api/{waits,threads,notify,system,schema}.go`, `cmd/umbrald/{stall,agents,waits,integrations}.go` and `cmd/umb/wait.go`.
- **REQ:** REQ-AUT-005, REQ-AUT-006, REQ-AUT-007, REQ-AUT-008

### [x] Approval
- Approved by the implementing agent under the Tech Lead's standing F1 instruction; the Tech Lead reviews it afterwards.
- Fold: API §2, §3, §4, §5.33, §6, §8 (1.38); Tech Design DD-017, §5.1, §7, §9.4, its version and history (1.48); the F1 tasks file with **Files** widened, the matrix rows and the execution log. Then archive.
