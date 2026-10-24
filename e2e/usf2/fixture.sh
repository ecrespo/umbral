#!/usr/bin/env bash
# US-F2-001's repository (PRD F2 §9): a Go module with a failing test, a stale README and a
# function to rename, committed on main. Usage: fixture.sh DIR
set -euo pipefail
DIR=$1
mkdir -p "$DIR"
cd "$DIR"
git init -q -b main
git config user.name "US-F2-001"
git config user.email "usf2@example.invalid"
cat >go.mod <<'GO'
module example.com/stats

go 1.22
GO
cat >stats.go <<'GO'
// Package stats has small numeric helpers.
package stats

// Mean is the arithmetic mean of xs, 0 for none.
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// Max is the largest of xs, 0 for none.
func Max(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}
GO
cat >stats_test.go <<'GO'
package stats

import "testing"

func TestMax(t *testing.T) {
	if got := Max([]float64{1, 5, 3}); got != 5 {
		t.Fatalf("Max = %v, want 5", got)
	}
}
GO
cat >report.go <<'GO'
package stats

import "fmt"

// Report describes xs in one line.
func Report(xs []float64) string {
	return fmt.Sprintf("n=%d mean=%.2f max=%.2f", len(xs), Mean(xs), Max(xs))
}
GO
cat >README.md <<'MD'
# stats

Small numeric helpers.

## Functions

- `Mean(xs)`: the arithmetic mean.
- `Sum(xs)`: the sum of the values.
MD
git add -A
git commit -q -m "initial"
