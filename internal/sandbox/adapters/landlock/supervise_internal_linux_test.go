package landlock

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestOnlyALoopbackAddressOnAnAllowedPortIsListenedOn_REQ_SBX_008: what the supervisor lets a
// confined process listen on, address by address. The kernel test cannot bind a port outside
// the profile to try it — Landlock refuses the bind first — so the port's half is held here.
func TestOnlyALoopbackAddressOnAnAllowedPortIsListenedOn_REQ_SBX_008(t *testing.T) {
	t.Parallel()
	ports := []uint16{4000, 4001}
	v6 := func(last byte, port int, mutate ...func(*[16]byte)) *unix.SockaddrInet6 {
		a := &unix.SockaddrInet6{Port: port}
		a.Addr[15] = last
		for _, m := range mutate {
			m(&a.Addr)
		}
		return a
	}
	for name, c := range map[string]struct {
		addr unix.Sockaddr
		want bool
	}{
		"the loopback on an allowed port":      {&unix.SockaddrInet4{Port: 4000, Addr: [4]byte{127, 0, 0, 1}}, true},
		"another loopback address":             {&unix.SockaddrInet4{Port: 4001, Addr: [4]byte{127, 9, 9, 9}}, true},
		"the IPv6 loopback":                    {v6(1, 4001), true},
		"the loopback on another port":         {&unix.SockaddrInet4{Port: 4002, Addr: [4]byte{127, 0, 0, 1}}, false},
		"the IPv6 loopback on another port":    {v6(1, 4002), false},
		"no bind: port 0 on every interface":   {&unix.SockaddrInet4{}, false},
		"no bind, IPv6":                        {v6(0, 0), false},
		"every interface on an allowed port":   {&unix.SockaddrInet4{Port: 4000}, false},
		"every interface, IPv6":                {v6(0, 4000), false},
		"an outward address":                   {&unix.SockaddrInet4{Port: 4000, Addr: [4]byte{192, 168, 1, 10}}, false},
		"an outward IPv6 address":              {v6(1, 4000, func(a *[16]byte) { a[0] = 0xfe; a[1] = 0x80 }), false},
		"the loopback mapped into IPv6":        {v6(1, 4000, func(a *[16]byte) { a[10], a[11], a[12], a[15] = 0xff, 0xff, 127, 1 }), false},
		"a port past sixteen bits":             {&unix.SockaddrInet4{Port: 4000 + 1<<16, Addr: [4]byte{127, 0, 0, 1}}, false},
		"a Unix-domain socket":                 {&unix.SockaddrUnix{Name: "/tmp/s"}, false},
		"a socket with no address to speak of": {nil, false},
	} {
		if got := loopbackPort(c.addr, ports); got != c.want {
			t.Errorf("%s: listened on = %v, want %v", name, got, c.want)
		}
	}
	if loopbackPort(&unix.SockaddrInet4{Port: 0, Addr: [4]byte{127, 0, 0, 1}}, []uint16{0}) {
		t.Error("an unbound loopback socket was listened on for a profile that names port 0")
	}
}

// TestOnlyADescendantIsAnswered_REQ_SBX_008: the supervisor answers the processes below it
// and no other. On a machine whose ptrace policy already stops it taking a stranger's
// descriptor the kernel test cannot tell this check from that policy, so it is held here.
func TestOnlyADescendantIsAnswered_REQ_SBX_008(t *testing.T) {
	t.Parallel()
	self, parent := os.Getpid(), os.Getppid()
	if !descends(self, self) || !descends(self, parent) {
		t.Error("this process does not descend from itself or from its parent")
	}
	if descends(parent, self) {
		t.Error("this process's parent descends from it")
	}
	if descends(1, self) || descends(0, self) || descends(1<<30, self) {
		t.Error("the first process, no process or one that does not exist descends from this one")
	}
}
