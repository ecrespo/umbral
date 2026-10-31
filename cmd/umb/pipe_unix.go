//go:build unix

package main

import (
	"errors"
	"syscall"
)

// isClosedPipe is a write to a stdout whose reader has gone.
func isClosedPipe(err error) bool { return errors.Is(err, syscall.EPIPE) }
