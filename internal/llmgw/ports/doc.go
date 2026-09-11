// Package ports is the ports layer of the llmgw module, which owns
// the model gateway: provider catalogue, routing, fallback and usage accounting.
//
// ports declares the interfaces the llmgw module publishes to the rest of the daemon.
// It may import its own domain and other modules' domain (Art. 3).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package ports
