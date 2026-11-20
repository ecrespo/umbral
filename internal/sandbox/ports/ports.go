// Package ports is what the sandbox offers the daemon: a way to run a command confined.
package ports

import "github.com/ecrespo/umbral/internal/sandbox/domain"

// Sandbox confines a process to a profile. An implementation that cannot — another operating
// system, a kernel without the mechanism — says so in Status and returns commands unchanged,
// so its caller discloses it rather than failing (REQ-SBX-003).
type Sandbox interface {
	// Status is the sandbox's state on this system.
	Status() domain.Status
	// Command returns the command to run instead of argv, so that the process is confined to
	// p before it runs any of argv's code. When Status is not enforcing it returns argv.
	Command(p domain.Profile, argv []string) ([]string, error)
}
