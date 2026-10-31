//go:build !linux && !darwin && !windows

package proctree

// Elsewhere nothing is listed, so nothing is detected.
func processTable() []entry { return nil }

func argvOf(int) []string { return nil }
