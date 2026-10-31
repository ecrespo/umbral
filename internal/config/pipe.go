package config

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// PipeName is the named pipe the daemon serves on Windows in place of the socket at socketPath
// (delta `2026-10-windows-port`, decision 3). It is derived from the runtime directory, so a
// client that knows the socket's path dials the same pipe, and two runtime directories — two
// test daemons — never share one.
func PipeName(socketPath string) string {
	dir := filepath.Clean(filepath.Dir(socketPath))
	sum := sha256.Sum256([]byte(dir))
	return `\\.\pipe\umbral-` + hex.EncodeToString(sum[:8])
}

// PipeBufferSize is each direction's buffer on the daemon's pipe. With none, every write waits
// for the reader to take it, and a burst of notifications crawls (REQ-WIN-001).
const PipeBufferSize = 64 << 10
