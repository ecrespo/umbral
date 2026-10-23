package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

// API Spec §5.34: the signed rule bundles' trust store, status and recovery (REQ-SEC-011,
// REQ-SEC-013 to REQ-SEC-016). Keys are named by fingerprint on the wire (DD-016).

const capabilityRules = "rules"

func hasRules(cfg Config) bool { return cfg.Rules != nil }

// rulesMethods is open to every client kind: `umb rules` is REQ-SEC-014's CLI.
func rulesMethods() map[string]method {
	return map[string]method{
		"rules.status":     {handle: handleRulesStatus, kinds: treeClients, params: emptyResult{}, result: rulesStatusResult{}},
		"rules.key.list":   {handle: handleRulesKeyList, kinds: treeClients, params: emptyResult{}, result: rulesKeyListResult{}},
		"rules.key.add":    {handle: handleRulesKeyAdd, userOnly: true, kinds: treeClients, params: rulesKeyAddParams{}, result: rulesKeyResult{}},
		"rules.key.remove": {handle: handleRulesKeyRemove, userOnly: true, kinds: treeClients, params: rulesKeyRemoveParams{}, result: rulesKeyRemoveResult{}},
		"rules.key.rotate": {handle: handleRulesKeyRotate, userOnly: true, kinds: treeClients, params: rulesKeyRotateParams{}, result: rulesKeyRotateResult{}},
		"rules.rollback":   {handle: handleRulesRollback, userOnly: true, kinds: treeClients, params: emptyResult{}, result: rulesStatusResult{}},
		"rules.reset":      {handle: handleRulesReset, userOnly: true, kinds: treeClients, params: emptyResult{}, result: rulesStatusResult{}},
	}
}

type rulesBundle struct {
	Version      int64   `json:"version"`
	SHA256       string  `json:"sha256"`
	Source       string  `json:"source"`
	VerifiedWith *string `json:"verified_with"`
}

type rulesPreviousBundle struct {
	Version int64  `json:"version"`
	SHA256  string `json:"sha256"`
}

type rulesTrustKey struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Source      string `json:"source"`
	AddedAt     int64  `json:"added_at"`
	RevokedAt   *int64 `json:"revoked_at"`
}

type rulesRejection struct {
	Reason string `json:"reason"`
	At     int64  `json:"at"`
}

type rulesStatusResult struct {
	ActiveBundle   rulesBundle          `json:"active_bundle"`
	PreviousBundle *rulesPreviousBundle `json:"previous_bundle"`
	LocalOverride  bool                 `json:"local_override"`
	RemoteUpdates  string               `json:"remote_updates"`
	TrustKeys      []rulesTrustKey      `json:"trust_keys"`
	LastRejection  *rulesRejection      `json:"last_rejection"`
}

type rulesKeyListResult struct {
	TrustKeys []rulesTrustKey `json:"trust_keys"`
}

type rulesKeyAddParams struct {
	PublicKey          string `json:"public_key"`
	ConfirmFingerprint string `json:"confirm_fingerprint"`
}

type rulesKeyResult struct {
	Key rulesTrustKey `json:"key"`
}

type rulesKeyRemoveParams struct {
	Fingerprint string `json:"fingerprint"`
	Force       bool   `json:"force" api:"optional"`
}

type rulesKeyRemoveResult struct {
	Key          rulesTrustKey `json:"key"`
	ActiveBundle rulesBundle   `json:"active_bundle"`
}

type rulesKeyRotateParams struct {
	Fingerprint        string `json:"fingerprint"`
	PublicKey          string `json:"public_key"`
	ConfirmFingerprint string `json:"confirm_fingerprint"`
}

type rulesKeyRotateResult struct {
	Added        rulesTrustKey `json:"added"`
	Revoked      rulesTrustKey `json:"revoked"`
	ActiveBundle rulesBundle   `json:"active_bundle"`
}

func toRulesBundle(b secdomain.BundleInfo) rulesBundle {
	out := rulesBundle{Version: b.Version, SHA256: b.SHA256, Source: b.Source}
	if b.VerifiedWith != "" {
		v := b.VerifiedWith
		out.VerifiedWith = &v
	}
	return out
}

func toRulesKey(k secdomain.KeyInfo) rulesTrustKey {
	return rulesTrustKey{ID: k.ID, Fingerprint: k.Fingerprint, Source: k.Source, AddedAt: k.AddedAt, RevokedAt: k.RevokedAt}
}

func toRulesKeys(keys []secdomain.KeyInfo) []rulesTrustKey {
	out := make([]rulesTrustKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, toRulesKey(k))
	}
	return out
}

func toRulesStatus(st secdomain.RulesStatus) rulesStatusResult {
	out := rulesStatusResult{
		ActiveBundle: toRulesBundle(st.Active), LocalOverride: st.LocalOverride,
		RemoteUpdates: st.RemoteUpdates, TrustKeys: toRulesKeys(st.TrustKeys),
	}
	if st.Previous != nil {
		out.PreviousBundle = &rulesPreviousBundle{Version: st.Previous.Version, SHA256: st.Previous.SHA256}
	}
	if st.LastRejection != "" {
		out.LastRejection = &rulesRejection{Reason: string(st.LastRejection), At: st.LastRejectionAt}
	}
	return out
}

func rulesService(c *conn, name string) error {
	if c.server.cfg.Rules == nil {
		return fmt.Errorf("%w: %s is not wired in", ErrNotImplemented, name)
	}
	return nil
}

func handleRulesStatus(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := rulesService(c, "rules.status"); err != nil {
		return nil, err
	}
	if err := decode(raw, &emptyResult{}, "rules.status"); err != nil {
		return nil, err
	}
	st, err := c.server.cfg.Rules.Status(ctx)
	if err != nil {
		return nil, err
	}
	return toRulesStatus(st), nil
}

func handleRulesKeyList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := rulesService(c, "rules.key.list"); err != nil {
		return nil, err
	}
	if err := decode(raw, &emptyResult{}, "rules.key.list"); err != nil {
		return nil, err
	}
	keys, err := c.server.cfg.Rules.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	return rulesKeyListResult{TrustKeys: toRulesKeys(keys)}, nil
}

func handleRulesKeyAdd(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := rulesService(c, "rules.key.add"); err != nil {
		return nil, err
	}
	var p rulesKeyAddParams
	if err := decode(raw, &p, "rules.key.add"); err != nil {
		return nil, err
	}
	k, err := c.server.cfg.Rules.AddKey(ctx, p.PublicKey, p.ConfirmFingerprint)
	if err != nil {
		return nil, err
	}
	return rulesKeyResult{Key: toRulesKey(k)}, nil
}

func handleRulesKeyRemove(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := rulesService(c, "rules.key.remove"); err != nil {
		return nil, err
	}
	var p rulesKeyRemoveParams
	if err := decode(raw, &p, "rules.key.remove"); err != nil {
		return nil, err
	}
	k, active, err := c.server.cfg.Rules.RemoveKey(ctx, p.Fingerprint, p.Force)
	if err != nil {
		return nil, err
	}
	return rulesKeyRemoveResult{Key: toRulesKey(k), ActiveBundle: toRulesBundle(active)}, nil
}

func handleRulesKeyRotate(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := rulesService(c, "rules.key.rotate"); err != nil {
		return nil, err
	}
	var p rulesKeyRotateParams
	if err := decode(raw, &p, "rules.key.rotate"); err != nil {
		return nil, err
	}
	added, revoked, active, err := c.server.cfg.Rules.RotateKey(ctx, p.Fingerprint, p.PublicKey, p.ConfirmFingerprint)
	if err != nil {
		return nil, err
	}
	return rulesKeyRotateResult{Added: toRulesKey(added), Revoked: toRulesKey(revoked), ActiveBundle: toRulesBundle(active)}, nil
}

func handleRulesRollback(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := rulesService(c, "rules.rollback"); err != nil {
		return nil, err
	}
	if err := decode(raw, &emptyResult{}, "rules.rollback"); err != nil {
		return nil, err
	}
	st, err := c.server.cfg.Rules.Rollback(ctx)
	if err != nil {
		return nil, err
	}
	return toRulesStatus(st), nil
}

func handleRulesReset(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	if err := rulesService(c, "rules.reset"); err != nil {
		return nil, err
	}
	if err := decode(raw, &emptyResult{}, "rules.reset"); err != nil {
		return nil, err
	}
	st, err := c.server.cfg.Rules.Reset(ctx)
	if err != nil {
		return nil, err
	}
	return toRulesStatus(st), nil
}

// ruleUpdateRejectedPayload is §6's `rules.update_rejected` (REQ-SEC-013). `version` is the
// bundle's own when its signature verified, 0 otherwise: unverified bytes are never parsed.
type ruleUpdateRejectedPayload struct {
	Reason  string `json:"reason"`
	Version int64  `json:"version"`
	Source  string `json:"source"`
}

// rulesDomainError maps the trust store's sentinels onto §3.
func rulesDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, secdomain.ErrInvalidPublicKey), errors.Is(err, secdomain.ErrFingerprintMismatch):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, secdomain.ErrUnknownKey):
		return codeNotFound, domainNotFound, true
	case errors.Is(err, secdomain.ErrKeyRevokedBySeed), errors.Is(err, secdomain.ErrLastValidKey), errors.Is(err, secdomain.ErrNoPreviousBundle):
		return codeConflict, domainConflict, true
	}
	return 0, "", false
}
