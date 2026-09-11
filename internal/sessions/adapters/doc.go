// Package adapters is the adapters layer of the sessions module, which owns
// PTY sessions, VT emulation through libghostty and the block lifecycle.
//
// adapters implements the sessions ports against the outside world.
// It may import its own ports, its own domain and external libraries (Art. 3).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package adapters
