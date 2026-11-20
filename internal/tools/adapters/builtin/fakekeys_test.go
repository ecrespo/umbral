package builtin

import (
	"context"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// fakeKeys is a thread's PTY that keeps the prompt's line the way the sessions module does.
type fakeKeys struct {
	state  sessdomain.InteractState
	sent   [][]sessdomain.Key
	expect []sessdomain.InteractState
	screen sessdomain.Screen
	cond   sessdomain.WaitCondition
	seq    uint64
	err    error
	// waitErr fails the wait alone, after the keys were sent.
	waitErr error
	// fullAuto is whether the last send carried the turn's full-auto mark.
	fullAuto bool
}

func (f *fakeKeys) InteractState(string, string) sessdomain.InteractState { return f.state }

func (f *fakeKeys) SendKeys(ctx context.Context, _, _ string, keys []sessdomain.Key, expect sessdomain.InteractState) (sessdomain.Plan, error) {
	f.fullAuto = sessdomain.FullAuto(ctx)
	if f.err != nil {
		return sessdomain.Plan{}, f.err
	}
	plan, err := sessdomain.PlanKeys(f.state, keys)
	if err != nil {
		return plan, err
	}
	f.sent, f.expect = append(f.sent, keys), append(f.expect, expect)
	f.seq += 10
	plan.Seq = f.seq
	if f.state.AtPrompt {
		f.state.Line = plan.Line
		if plan.Submit {
			f.state = sessdomain.InteractState{}
		}
	} else {
		f.state.Program = plan.Program
	}
	return plan, nil
}

func (f *fakeKeys) ReadScreen(context.Context, string, string) (sessdomain.Screen, error) {
	return f.screen, f.err
}

func (f *fakeKeys) WaitFor(_ context.Context, _, _ string, cond sessdomain.WaitCondition) (sessdomain.WaitOutcome, error) {
	f.cond = cond
	if f.waitErr != nil {
		return sessdomain.WaitOutcome{}, f.waitErr
	}
	return sessdomain.WaitOutcome{Reason: sessdomain.WaitMatched, Screen: f.screen}, f.err
}
