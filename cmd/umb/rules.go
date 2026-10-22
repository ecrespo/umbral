package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	secdomain "github.com/ecrespo/umbral/internal/security/domain"
)

type rulesBundle struct {
	Version      int64   `json:"version"`
	SHA256       string  `json:"sha256"`
	Source       string  `json:"source"`
	VerifiedWith *string `json:"verified_with"`
}

type rulesKey struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Source      string `json:"source"`
	AddedAt     int64  `json:"added_at"`
	RevokedAt   *int64 `json:"revoked_at"`
}

type rulesStatus struct {
	ActiveBundle   rulesBundle `json:"active_bundle"`
	PreviousBundle *struct {
		Version int64  `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"previous_bundle"`
	LocalOverride bool       `json:"local_override"`
	RemoteUpdates string     `json:"remote_updates"`
	TrustKeys     []rulesKey `json:"trust_keys"`
	LastRejection *struct {
		Reason string `json:"reason"`
		At     int64  `json:"at"`
	} `json:"last_rejection"`
}

// cmdRules is `umb rules` (REQ-SEC-014, REQ-SEC-016, API Spec §5.34): the signed rule
// bundles' status, the trust store and offline recovery. Adding or rotating a key needs the
// key's fingerprint typed back with --confirm; without it the fingerprint is printed and
// nothing is sent.
func cmdRules(ctx context.Context, args []string, stdout, stderr *printer) int {
	const usageText = "umb rules: status | key list | key add PUBKEY --confirm FP | key remove FP [--force] | key rotate FP PUBKEY --confirm FP | rollback | reset"
	if len(args) == 0 {
		stderr.println(usageText)
		return exitFailure
	}
	name := args[0]
	rest := args[1:]
	if name == "key" {
		if len(rest) == 0 {
			stderr.println(usageText)
			return exitFailure
		}
		name, rest = "key "+rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("umb rules "+name, flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	confirm := fs.String("confirm", "", "the key's fingerprint, typed back to confirm it")
	force := fs.Bool("force", false, "remove the last valid key")
	pos, more, err := parseInterleaved(fs, rest)
	if err != nil {
		return exitFailure
	}
	pos = append(pos, more...)

	var params map[string]any
	method, plain := map[string]string{"status": "rules.status", "key list": "rules.key.list", "rollback": "rules.rollback", "reset": "rules.reset"}[name]
	switch {
	case plain && len(pos) == 0:
	case name == "key add" && len(pos) == 1:
		if !confirmed(pos[0], *confirm, "umb rules key add", f.asJSON, stdout, stderr) {
			return exitFailure
		}
		method, params = "rules.key.add", map[string]any{"public_key": pos[0], "confirm_fingerprint": *confirm}
	case name == "key remove" && len(pos) == 1:
		method, params = "rules.key.remove", map[string]any{"fingerprint": pos[0]}
		if *force {
			params["force"] = true
		}
	case name == "key rotate" && len(pos) == 2:
		if !confirmed(pos[1], *confirm, "umb rules key rotate", f.asJSON, stdout, stderr) {
			return exitFailure
		}
		method, params = "rules.key.rotate", map[string]any{"fingerprint": pos[0], "public_key": pos[1], "confirm_fingerprint": *confirm}
	default:
		stderr.println(usageText)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	switch method {
	case "rules.status", "rules.rollback", "rules.reset":
		var st rulesStatus
		if err := c.Call(callCtx, method, map[string]any{}, &st); err != nil {
			return callFailed(stderr, "umb rules "+name+": ", err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, st)
		}
		printRulesStatus(stdout, st)
	case "rules.key.list":
		var out struct {
			TrustKeys []rulesKey `json:"trust_keys"`
		}
		if err := c.Call(callCtx, method, map[string]any{}, &out); err != nil {
			return callFailed(stderr, "umb rules key list: ", err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		printKeys(stdout, out.TrustKeys)
	default:
		var out map[string]any
		if err := c.Call(callCtx, method, params, &out); err != nil {
			return callFailed(stderr, "umb rules "+name+": ", err)
		}
		if f.asJSON {
			return printJSON(stdout, stderr, out)
		}
		switch method {
		case "rules.key.add":
			stdout.printf("trusted %s\n", *confirm)
		case "rules.key.remove":
			stdout.printf("revoked %s\n", pos[0])
		case "rules.key.rotate":
			stdout.printf("trusted %s, revoked %s\n", *confirm, pos[0])
		}
	}
	return exitOK
}

// confirmed is REQ-SEC-014's explicit confirmation: the key's fingerprint typed back. Without
// it, or with another, the fingerprint is shown and nothing is sent.
func confirmed(publicKey, confirm, cmd string, asJSON bool, stdout, stderr *printer) bool {
	pub, err := secdomain.ParsePublicKey(publicKey)
	if err != nil {
		stderr.printf("%s: %v\n", cmd, err)
		return false
	}
	fp := secdomain.Fingerprint(pub)
	if confirm == fp {
		return true
	}
	if asJSON {
		out, _ := json.Marshal(struct {
			Fingerprint string `json:"fingerprint"`
		}{fp})
		stdout.printf("%s\n", out)
	} else {
		stdout.printf("fingerprint: %s\n", fp)
	}
	if confirm == "" {
		stderr.printf("%s: check the fingerprint against the one published, then run again with --confirm %s\n", cmd, fp)
	} else {
		stderr.printf("%s: --confirm %s is not this key's fingerprint\n", cmd, confirm)
	}
	return false
}

func printRulesStatus(stdout *printer, st rulesStatus) {
	stdout.printf("active: version %d (%s) %s\n", st.ActiveBundle.Version, st.ActiveBundle.Source, st.ActiveBundle.SHA256)
	if st.PreviousBundle != nil {
		stdout.printf("previous: version %d %s\n", st.PreviousBundle.Version, st.PreviousBundle.SHA256)
	}
	stdout.printf("local overrides: %v\nremote updates: %s\n", st.LocalOverride, st.RemoteUpdates)
	if st.LastRejection != nil {
		stdout.printf("last rejection: %s at %s\n", st.LastRejection.Reason, time.UnixMilli(st.LastRejection.At).UTC().Format(time.RFC3339))
	}
	printKeys(stdout, st.TrustKeys)
}

func printKeys(stdout *printer, keys []rulesKey) {
	if len(keys) == 0 {
		stdout.println("no trust keys")
		return
	}
	for _, k := range keys {
		state := "valid"
		if k.RevokedAt != nil {
			state = fmt.Sprintf("revoked %s", time.UnixMilli(*k.RevokedAt).UTC().Format(time.RFC3339))
		}
		stdout.printf("%s  %s  %s  %s\n", k.Fingerprint, k.ID, k.Source, state)
	}
}
