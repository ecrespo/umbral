// Package ports is the ports layer of the sessions module, which owns
// PTY sessions, VT emulation through libghostty and the block lifecycle.
//
// ports declares the interfaces the sessions module publishes to the rest of the daemon.
// It may import its own domain and other modules' domain (Art. 3).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package ports
