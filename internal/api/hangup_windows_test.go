package api

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isHangUp is a write that failed because the daemon had already hung up: "the pipe is being
// closed" or "the pipe has been ended".
func isHangUp(err error) bool {
	return errors.Is(err, windows.ERROR_NO_DATA) || errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED)
}
