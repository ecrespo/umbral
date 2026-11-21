// Package seatbelt confines a command on macOS with `sandbox-exec` and a profile generated
// for it (delta `2026-11-sandbox-macos`, T-HRD-16). The kernel's sandbox is applied by Apple's
// own tool before the command runs, so no code of the command's runs unconfined and nothing
// here needs a helper.
package seatbelt

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// Mechanism is what `system.status` names as enforcing.
const Mechanism = "seatbelt"

// Tool is Apple's launcher. It is named by its full path: a `sandbox-exec` found through PATH
// could be anyone's.
const Tool = "/usr/bin/sandbox-exec"

// resolver is the one local socket a confined process may connect to: without it no name
// resolves (measured on macOS 26, docs/spikes/2026-11-seatbelt.md).
const resolver = "/private/var/run/mDNSResponder"

// services are the Mach services a confined process may look up: the ones a program needs to
// log, to learn who its user is, to resolve a name and to check a certificate. Every other
// lookup is refused — the preferences daemon, the keychain, Launch Services and the rest of
// the user's session among them. A shell, git, a name lookup and an HTTPS request were
// measured to work with all of them refused; these are margin for programs that were not.
var services = []string{
	"com.apple.system.notification_center",
	"com.apple.system.logger",
	"com.apple.logd",
	"com.apple.diagnosticd",
	"com.apple.bsd.dirhelper",
	"com.apple.system.opendirectoryd.libinfo",
	"com.apple.system.opendirectoryd.membership",
	"com.apple.dnssd.service",
	"com.apple.SystemConfiguration.DNSConfiguration",
	"com.apple.SystemConfiguration.configd",
	"com.apple.trustd",
	"com.apple.trustd.agent",
	"com.apple.ocspd",
}

// networked are the services among them that reach the network on their caller's behalf: name
// resolution, the network's configuration and certificate checks. A profile that names a
// network gets none of them, so nothing is resolved and no certificate is checked under one.
var networked = map[string]bool{
	"com.apple.dnssd.service":                        true,
	"com.apple.SystemConfiguration.DNSConfiguration": true,
	"com.apple.SystemConfiguration.configd":          true,
	"com.apple.trustd":                               true,
	"com.apple.trustd.agent":                         true,
	"com.apple.ocspd":                                true,
}

// Sandbox is the macOS adapter of the sandbox port.
type Sandbox struct{ status domain.Status }

// New probes the system once: the status does not change while the daemon runs.
func New() *Sandbox { return &Sandbox{status: probe()} }

// Status says whether a command given to Command is confined.
func (s *Sandbox) Status() domain.Status { return s.status }

// Command is argv run through sandbox-exec under p's rules; argv itself where the sandbox
// does not enforce.
func (s *Sandbox) Command(p domain.Profile, argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, errors.New("sandbox: no command to confine")
	}
	if p.Net != nil && (!s.status.Enforcing() || !s.status.Network) {
		// A network is never dropped from a profile in silence.
		return nil, domain.ErrNoNetwork
	}
	if !s.status.Enforcing() {
		return argv, nil
	}
	// sandbox-exec reads its own flags up to the command's name: one that starts with a dash
	// would be read as a flag of the tool's.
	if strings.HasPrefix(argv[0], "-") {
		return nil, fmt.Errorf("sandbox: %q cannot be run as a command", argv[0])
	}
	text, err := Render(p)
	if err != nil {
		return nil, err
	}
	return append([]string{Tool, "-p", text}, argv...), nil
}

// Render is p as a sandbox profile. The last rule that matches an operation decides it, so
// each refusal is written before what it leaves open.
//
//   - Writes: refused everywhere, then allowed in p.Write and on the device files.
//   - Reads: refused in p.DenyRead — a file's metadata with its content, so a denied path
//     cannot even be probed — then allowed again in a Write entry inside one, and the
//     directories above such an entry answer `stat`, which resolving a path needs.
//   - Local sockets: no connection to one, as on Linux, where a process reached the user's
//     session services through them. The resolver's stays.
//   - The network, when the profile names one: refused whole, then allowed on the loopback's
//     ports it names. Without one it is left open.
//   - Launch Services, Apple events and launchd jobs: refused. Each starts a process outside
//     the sandbox for the caller.
//   - Mach services: every lookup refused but the few in services.
//   - Signals: only to a process in the same sandbox, the command's own children.
func Render(p domain.Profile) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	// Every path is checked, whether or not a rule comes of it below.
	for _, list := range [][]string{p.Write, p.DenyRead, p.Read} {
		for _, path := range list {
			if err := rule(&strings.Builder{}, "", "", path); err != nil {
				return "", err
			}
		}
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n")
	for _, w := range p.Write {
		if err := rule(&b, "allow file-write*", "subpath", w); err != nil {
			return "", err
		}
	}
	for _, d := range domain.Devices {
		if err := rule(&b, "allow file-write*", "literal", d); err != nil {
			return "", err
		}
	}
	for _, d := range p.DenyRead {
		if err := rule(&b, "deny file-read*", "subpath", d); err != nil {
			return "", err
		}
	}
	// What is read again inside a denied directory: the write entries there, and the read
	// entries, which get no write rule.
	reopened := append(append([]string(nil), p.Write...), p.Read...)
	for _, d := range p.DenyRead {
		for _, w := range reopened {
			if !domain.Within(w, d) {
				continue
			}
			if err := rule(&b, "allow file-read*", "subpath", w); err != nil {
				return "", err
			}
			for dir := filepath.Dir(w); domain.Within(dir, d); dir = filepath.Dir(dir) {
				if err := rule(&b, "allow file-read-metadata", "literal", dir); err != nil {
					return "", err
				}
			}
		}
	}
	if p.Net == nil {
		b.WriteString("(deny network-outbound (remote unix-socket))\n")
		if err := rule(&b, "allow network-outbound", "literal", resolver); err != nil {
			return "", err
		}
	} else {
		// All of it refused — every protocol, the local sockets and the resolver's with them —
		// and then the loopback's ports the profile names. Unlike Landlock's, these rules name
		// the host: a port is not opened on any other.
		b.WriteString("(deny network*)\n")
		// TCP by name: an `ip` filter would open the same port to a datagram.
		for _, port := range p.Net.Connect {
			fmt.Fprintf(&b, "(allow network-outbound (remote tcp \"localhost:%d\"))\n", port)
		}
		for _, port := range p.Net.Bind {
			fmt.Fprintf(&b, "(allow network-bind (local tcp \"localhost:%d\"))\n", port)
			// A `local` filter's host is not checked — a bind on every interface passes it, and
			// so does a connection to the machine's outward address (measured on the runner) —
			// so a connection is let in only from a loopback peer, which a `remote` filter checks.
			// `listen` is an inbound check with no peer, where a `remote` filter never matches:
			// the port is allowed, every peer refused, and the loopback's let back in; the last
			// matching rule wins.
			fmt.Fprintf(&b, "(allow network-inbound (local tcp \"localhost:%d\"))\n", port)
			fmt.Fprintf(&b, "(deny network-inbound (require-all (local tcp \"localhost:%d\") (remote tcp \"*:*\")))\n", port)
			fmt.Fprintf(&b, "(allow network-inbound (require-all (local tcp \"localhost:%d\") (remote tcp \"localhost:*\")))\n", port)
		}
	}
	b.WriteString("(deny lsopen)\n(deny appleevent-send)\n(deny job-creation)\n")
	b.WriteString("(deny mach-lookup)\n")
	for _, name := range services {
		// A process without a network is not left a service that uses one for it: the
		// resolver would carry a name out, and the trust daemons fetch what a certificate
		// names.
		if p.Net != nil && networked[name] {
			continue
		}
		fmt.Fprintf(&b, "(allow mach-lookup (global-name %q))\n", name)
	}
	b.WriteString("(deny signal)\n(allow signal (target same-sandbox))\n")
	return b.String(), nil
}

// rule writes `(<verb> (<filter> "<path>"))`. A path is quoted as the profile language's
// strings are; one holding a control character is refused rather than guessed at.
func rule(b *strings.Builder, verb, filter, path string) error {
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("sandbox: %q holds a control character and cannot be written in a profile", path)
		}
	}
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path)
	fmt.Fprintf(b, "(%s (%s \"%s\"))\n", verb, filter, quoted)
	return nil
}
