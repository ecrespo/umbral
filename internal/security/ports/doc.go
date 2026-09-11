// Package ports is the ports layer of the security module, which owns
// the policy engine, secret redaction and the egress audit trail.
//
// ports declares the interfaces the security module publishes to the rest of the daemon.
// It may import its own domain and other modules' domain (Art. 3).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package ports
