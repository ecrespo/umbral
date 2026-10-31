//go:build !linux

package sshtunnel

import "syscall"

// sysProcAttr has no parent-death signal outside Linux: a kill -9 of umbrald there leaves its
// ssh forwards running until their link drops, and the next daemon's sweep removes their sockets.
func sysProcAttr() *syscall.SysProcAttr { return nil }
