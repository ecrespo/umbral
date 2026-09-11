// Package bus is the daemon's typed in-process pub/sub (Tech Design §3, DD-005).
// Modules never call each other directly for events: they publish and subscribe here.
//
// Scaffolding for T-F0-01; the implementation arrives with its own task.
package bus
