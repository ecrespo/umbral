#!/usr/bin/env bash
# US-003, live (T-F1-21): the reference scenario run UMBRAL_US003_RUNS times (20 by default)
# against an isolated daemon and the local model, offline, writing the report to
# docs/reports/us003-<date>.md (or UMBRAL_US003_REPORT). It needs Ollama with the model pulled
# (UMBRAL_LIVE_MODEL, gpt-oss:20b by default; UMBRAL_OLLAMA_URL, 127.0.0.1:11434 by default)
# and libghostty-vt (task deps:ghostty). The daemon it starts is its own: every XDG directory
# is redirected, and the developer's socket and database are never touched.
set -euo pipefail
cd "$(dirname "$0")/../.."
export PKG_CONFIG_PATH="${PKG_CONFIG_PATH:-$HOME/.local/ghostty-vt/share/pkgconfig}"
mkdir -p docs/reports
exec go test -tags live -count=1 -timeout 8h -run '^TestUS003FixItOffline_REQ_AGT_001$' -v ./cmd/umbrald/ "$@"
