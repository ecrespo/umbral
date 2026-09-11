// Package config loads and validates the daemon's TOML configuration.
// Secrets are only ever read as `keyring:<path>` references (Art. 5).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package config
