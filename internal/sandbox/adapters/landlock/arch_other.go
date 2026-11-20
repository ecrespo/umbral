//go:build linux && !amd64 && !arm64

package landlock

// auditArch is zero where the seccomp filter's architecture is not known: the sandbox is then
// unavailable, since it could not refuse Unix-domain sockets.
const auditArch = 0
