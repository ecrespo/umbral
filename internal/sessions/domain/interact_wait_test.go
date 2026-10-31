package domain

import (
	"errors"
	"regexp"
	"testing"
	"time"
)

// TestAWaitMayCombineConditions_REQ_FTU_001: a wait takes one or more of a regex, a quiet
// stretch and the program's exit, and ends at the first that holds — a model asks for "the
// prompt, or two seconds of quiet" in one call (delta `2026-10-usf2-002-live`). No condition,
// or a duration out of range, is still refused.
func TestAWaitMayCombineConditions_REQ_FTU_001(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`postgres=#`)
	for _, ok := range []WaitCondition{
		{Regex: re, Timeout: time.Second},
		{Regex: re, Idle: 2 * time.Second, Timeout: 10 * time.Second},
		{Regex: re, Idle: 2 * time.Second, Exit: true, Timeout: 10 * time.Second},
		{Idle: MinWaitIdle, Exit: true, Timeout: time.Second},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%+v refused: %v", ok, err)
		}
	}
	for _, bad := range []WaitCondition{
		{Timeout: time.Second},
		{Regex: re, Idle: MinWaitIdle - 1, Timeout: time.Second},
		{Regex: re, Timeout: MaxWaitTime + 1},
		{Regex: re},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrValidation) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
}
