package main

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// isClosedPipe is a write to a stdout whose reader has gone: on Windows "the pipe is being
// closed" or "the pipe has been ended" rather than EPIPE.
func isClosedPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, windows.ERROR_NO_DATA) ||
		errors.Is(err, windows.ERROR_BROKEN_PIPE)
}
