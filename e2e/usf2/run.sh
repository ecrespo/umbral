#!/usr/bin/env bash
# T-F2-12: US-F2-001 (PRD F2 §9) and the F2a budgets NFR-F2-01, 02 and 03, against a real daemon,
# the local gpt-oss:20b and OpenCode over ACP (cmd/umbrald/usf2_live_test.go and
# usf2_nfr_live_test.go, tag `live`). Nothing else may be using Ollama during the run.
# UMBRAL_OPENCODE names the OpenCode binary when it is not on PATH or in ~/.opencode/bin.
set -euo pipefail
REPO=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
cd "$REPO"
export PKG_CONFIG_PATH=${PKG_CONFIG_PATH:-$HOME/.local/ghostty-vt/share/pkgconfig}
go test -tags live -count=1 -timeout 120m -v ./cmd/umbrald/ \
	-run 'TestLiveWorktreeCreateOn1GiB_NFR_F2_01|TestLiveIdleThreadOverhead_NFR_F2_02|TestLiveACPMediationLatency_NFR_F2_03|TestLiveUSF2001_REQ_ORC_001' \
	"$@"
