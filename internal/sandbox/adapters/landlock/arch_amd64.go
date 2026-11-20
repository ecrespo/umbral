//go:build linux

package landlock

// auditArch is AUDIT_ARCH_X86_64, the architecture the seccomp filter accepts system calls of.
const auditArch = 0xc000003e
