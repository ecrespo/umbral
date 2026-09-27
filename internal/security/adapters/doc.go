// Package adapters is the adapters layer of the security module, which owns
// the policy engine, secret redaction and the egress audit trail.
//
// adapters implements the security ports against the outside world.
// It may import its own ports, its own domain and external libraries (Art. 3).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package adapters
