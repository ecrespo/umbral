// Package integration holds the worktree engine's tests against a real git.
//
// They are integration tests on purpose: a merge that leaves both copies as they were, a hook
// that rejects a commit and a signing prompt that never answers are statements about what git
// does, which a fake would only restate.
package integration
