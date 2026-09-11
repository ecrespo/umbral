// Package adapters is the adapters layer of the llmgw module, which owns
// the model gateway: provider catalogue, routing, fallback and usage accounting.
//
// adapters implements the llmgw ports against the outside world.
// It may import its own ports, its own domain and external libraries (Art. 3).
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package adapters
