#!/usr/bin/env bash
# Proves that the Art. 3 gate has teeth: it injects the forbidden import named in the
# T-F0-01 done criterion (sessions reaching into agents), asserts that `go-arch-lint`
# rejects it, and removes it again.
#
# Run by `task arch:selftest` and by the CI pipeline. Without this, a broken
# .go-arch-lint.yml would pass silently and every later boundary violation with it.
set -euo pipefail

cd "$(dirname "$0")/.."

PROBE="internal/sessions/adapters/zz_arch_selftest_probe.go"
cleanup() { rm -f "$PROBE"; }
trap cleanup EXIT

if ! go-arch-lint check >/dev/null 2>&1; then
  echo "arch:selftest: the project already violates its own rules; fix that first" >&2
  go-arch-lint check >&2 || true
  exit 1
fi

cat > "$PROBE" <<'PROBE_EOF'
package adapters

// Deliberate Art. 3 violation, injected by scripts/arch_selftest.sh.
// sessions must never depend on agents (Tech Design §5.2).
import _ "github.com/ecrespo/umbral/internal/agents/ports"
PROBE_EOF

if go-arch-lint check >/dev/null 2>&1; then
  echo "arch:selftest: FAILED — sessions -> agents was accepted; the rules are not enforcing anything" >&2
  exit 1
fi

echo "arch:selftest: OK — the forbidden import sessions -> agents is rejected"
