package domain

import "context"

type fullAutoKey struct{}

// WithFullAuto marks ctx as a call of a turn that decides under `full-auto` (REQ-SBX-006).
// The turn read its mode once, at its start, and let the command run unasked on that
// reading; the thread's shell is confined on the same one, whatever the thread's record
// says by the time the command runs.
func WithFullAuto(ctx context.Context) context.Context {
	return context.WithValue(ctx, fullAutoKey{}, true)
}

// FullAuto reports whether ctx carries WithFullAuto's mark.
func FullAuto(ctx context.Context) bool {
	marked, _ := ctx.Value(fullAutoKey{}).(bool)
	return marked
}
