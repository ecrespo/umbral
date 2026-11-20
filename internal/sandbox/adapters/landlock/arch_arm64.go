//go:build linux

package landlock

// auditArch is AUDIT_ARCH_AARCH64, the architecture the seccomp filter accepts system calls of.
const auditArch = 0xc00000b7
