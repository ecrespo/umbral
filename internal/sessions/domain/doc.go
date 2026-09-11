// Package domain is the domain layer of the sessions module, which owns
// PTY sessions, VT emulation through libghostty and the block lifecycle.
//
// domain holds the pure types and rules of the sessions module.
// It imports nothing outside the standard library and other modules' domain (Art. 3).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package domain
