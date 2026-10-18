# Tasks — delta `2026-10-signed-rule-bundles`

### [x] T-F1-30 · Signed rule bundles and the trust store
- **What:** decisions 1–10 in `internal/security/domain/{bundle,rulestate}.go`, `internal/security/rules.go`, `internal/security/adapters/rules/{store,fetch,active}.go` and `trustseed/`, `internal/store/migrations/0006_rule_state.sql`, `internal/api/rules.go`, `cmd/umbrald/rules.go`, `cmd/umb/rules.go` and `tools/rulesign/`.
- **REQ:** REQ-SEC-011, REQ-SEC-013, REQ-SEC-014, REQ-SEC-015, REQ-SEC-016

### [x] Approval
- Approved by the implementing agent under the Tech Lead's standing F1 instruction; the Tech Lead reviews it afterwards.
- Fold: API §2, §5.34, §6 (1.37); Tech Design DD-016, §5.2, §9.4, its version and history (1.47); Data Model §2.4e, §2.12, §5 (1.22); the F1 tasks file with **Files** widened, the matrix rows and the execution log. Then archive.
