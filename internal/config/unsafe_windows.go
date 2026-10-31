package config

import "unsafe"

// unsafePointer reads an ACE's trailing SID, which Windows lays out in place of SidStart.
func unsafePointer(p *uint32) unsafe.Pointer { return unsafe.Pointer(p) }
