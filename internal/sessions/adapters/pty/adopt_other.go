//go:build !linux && !darwin

package pty

import "errors"

// exitWatch has no implementation here: a handoff is Linux and macOS only.
type exitWatch struct{}

func watchExit(int) (exitWatch, error) {
	return exitWatch{}, errors.New("pty: a handoff needs Linux or macOS")
}

func (exitWatch) wait() error { return errors.New("pty: a handoff needs Linux or macOS") }

func (exitWatch) close() {}
