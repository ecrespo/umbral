// Package integration holds the sessions module's integration tests.
//
// They live in their own directory because of what they are: a composition root. Wiring
// the real PTY, the real emulator and the real shell bootstrap together is the job
// Art. 3 reserves for cmd, and a test that does it is doing the same thing. Keeping it
// here lets `.go-arch-lint.yml` say so explicitly, instead of excluding every _test.go
// from the boundary rules and quietly giving up on enforcing them in test code.
//
// Tests that need no adapters belong beside the code they exercise.
package integration
