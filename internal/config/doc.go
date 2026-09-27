// Package config answers two questions for the daemon and its clients: where the runtime
// files live, and what the TOML configuration says.
//
// The first is implemented (`paths.go`): it is here rather than in `api` because both
// sides of the socket need the same answer and the boundary rules of Tech Design §5.2 let
// `api` and `client` depend on `config` but not on each other.
//
// The second arrives with T-F1-02. Secrets will only ever be read as `keyring:<path>`
// references, or as `env:<VAR>` when the keyring is unavailable and the fallback is
// enabled explicitly (Art. 5, amendment of 2026-09-20).
package config
