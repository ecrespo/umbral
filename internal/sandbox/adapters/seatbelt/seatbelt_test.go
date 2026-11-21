package seatbelt

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/sandbox/domain"
)

// agent is a hosted agent's profile as the daemon makes it: a worktree inside the data
// directory, a root inside the runtime directory, one `[sandbox] write` entry outside both.
var agent = domain.Profile{
	Write:    []string{"/data/worktrees/r/thr_1", "/run/umbral/acp/thr_1", "/Users/u/Library/Caches/go-build"},
	DenyRead: []string{"/data", "/run/umbral"},
}

// posixPaths skips a test whose profile is written with POSIX paths: `/data` is not absolute
// on Windows, where this adapter never renders one.
func posixPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the profile's paths are POSIX paths; sandbox-exec is macOS's")
	}
}

// order fails unless each of lines appears in text, in that order.
func order(t *testing.T, text string, lines ...string) {
	t.Helper()
	at := 0
	for _, line := range lines {
		i := strings.Index(text[at:], line+"\n")
		if i < 0 {
			t.Fatalf("the profile lacks %q after offset %d:\n%s", line, at, text)
		}
		at += i + len(line)
	}
}

// TestTheProfileRefusesEveryWriteButItsOwn_REQ_SBX_001: the last matching rule decides, so the
// refusal of every write comes first and the profile's own directories and the device files
// after it; nothing else is allowed a write.
func TestTheProfileRefusesEveryWriteButItsOwn_REQ_SBX_001(t *testing.T) {
	posixPaths(t)
	text, err := Render(agent)
	if err != nil {
		t.Fatal(err)
	}
	order(t, text, "(version 1)", "(allow default)", "(deny file-write*)",
		`(allow file-write* (subpath "/data/worktrees/r/thr_1"))`,
		`(allow file-write* (subpath "/run/umbral/acp/thr_1"))`,
		`(allow file-write* (subpath "/Users/u/Library/Caches/go-build"))`,
		`(allow file-write* (literal "/dev/null"))`,
		`(allow file-write* (literal "/dev/tty"))`)
	var allowed []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "(allow file-write") {
			allowed = append(allowed, line)
		}
	}
	if want := len(agent.Write) + len(domain.Devices); len(allowed) != want {
		t.Errorf("%d write rules, want %d: %v", len(allowed), want, allowed)
	}
	if strings.Count(text, "(deny file-write*)") != 1 || strings.Contains(text, "(allow file-write*)\n") {
		t.Errorf("the write refusal is not the one rule expected:\n%s", text)
	}
}

// TestTheProfileDeniesUmbralsDirectoriesAndReopensItsOwn_REQ_SBX_002: Umbral's directories are
// refused to read, metadata included; the thread's own worktree and root, which live inside
// them, are read again by a later rule, and each directory above one answers `stat` and no
// more, so the path to it resolves. A write entry outside a denied directory needs neither.
func TestTheProfileDeniesUmbralsDirectoriesAndReopensItsOwn_REQ_SBX_002(t *testing.T) {
	posixPaths(t)
	text, err := Render(agent)
	if err != nil {
		t.Fatal(err)
	}
	order(t, text,
		`(deny file-read* (subpath "/data"))`,
		`(deny file-read* (subpath "/run/umbral"))`,
		`(allow file-read* (subpath "/data/worktrees/r/thr_1"))`)
	order(t, text, `(deny file-read* (subpath "/run/umbral"))`, `(allow file-read* (subpath "/run/umbral/acp/thr_1"))`)
	var meta []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "(allow file-read-metadata") {
			meta = append(meta, line)
		}
	}
	slices.Sort(meta)
	want := []string{
		`(allow file-read-metadata (literal "/data"))`,
		`(allow file-read-metadata (literal "/data/worktrees"))`,
		`(allow file-read-metadata (literal "/data/worktrees/r"))`,
		`(allow file-read-metadata (literal "/run/umbral"))`,
		`(allow file-read-metadata (literal "/run/umbral/acp"))`,
	}
	if !slices.Equal(meta, want) {
		t.Errorf("metadata rules = %v, want %v", meta, want)
	}
	if strings.Contains(text, `(allow file-read* (subpath "/Users/u/Library/Caches/go-build"))`) {
		t.Error("a write entry outside every denied directory was given a read rule it does not need")
	}
	if strings.Count(text, "(allow file-read* ") != 2 {
		t.Errorf("read is reopened somewhere else than the thread's two directories:\n%s", text)
	}
}

// TestTheProfileRefusesWaysOutOfTheSandbox_REQ_SBX_001: a connection to a local socket, a
// Launch Services open, an Apple event and a launchd job each run something outside the
// sandbox for the caller. All four are refused; the resolver's socket, without which no name
// resolves, is allowed after the refusal.
func TestTheProfileRefusesWaysOutOfTheSandbox_REQ_SBX_001(t *testing.T) {
	posixPaths(t)
	text, err := Render(agent)
	if err != nil {
		t.Fatal(err)
	}
	order(t, text, "(deny network-outbound (remote unix-socket))", `(allow network-outbound (literal "/private/var/run/mDNSResponder"))`)
	for _, line := range []string{"(deny lsopen)", "(deny appleevent-send)", "(deny job-creation)"} {
		order(t, text, line)
	}
	if strings.Count(text, "(allow network") != 1 {
		t.Errorf("more than the resolver's socket is allowed:\n%s", text)
	}
}

// TestTheProfileRefusesTheSessionsServicesAndSignals_REQ_SBX_001: every Mach lookup is refused
// before the few a program needs are allowed — never the preferences daemon, the keychain or
// Launch Services — and a signal reaches only a process in the same sandbox.
func TestTheProfileRefusesTheSessionsServicesAndSignals_REQ_SBX_001(t *testing.T) {
	posixPaths(t)
	text, err := Render(agent)
	if err != nil {
		t.Fatal(err)
	}
	order(t, text, "(deny mach-lookup)", `(allow mach-lookup (global-name "com.apple.trustd"))`)
	order(t, text, "(deny mach-lookup)", `(allow mach-lookup (global-name "com.apple.system.opendirectoryd.libinfo"))`)
	order(t, text, "(deny signal)", "(allow signal (target same-sandbox))")
	if strings.Count(text, "(deny mach-lookup)\n") != 1 || strings.Count(text, "(deny signal)\n") != 1 || strings.Count(text, "(allow signal") != 1 {
		t.Errorf("the refusals are not the ones expected:\n%s", text)
	}
	if got := strings.Count(text, "(allow mach-lookup "); got != len(services) {
		t.Errorf("%d lookups allowed, want the %d listed", got, len(services))
	}
	for _, name := range []string{"cfprefsd", "SecurityServer", "securityd", "secd", "launchservicesd", "lsd", "coreservices", "pasteboard", "tccd", "windowserver"} {
		for _, s := range services {
			if strings.Contains(strings.ToLower(s), strings.ToLower(name)) {
				t.Errorf("%s is allowed: it reaches %s", s, name)
			}
		}
	}
}

// TestAReadEntryIsReopenedForReadingOnly_REQ_SBX_006: a read entry inside a denied directory is
// allowed to read after the refusal, its ancestors answer `stat`, and it gets no write rule.
func TestAReadEntryIsReopenedForReadingOnly_REQ_SBX_006(t *testing.T) {
	posixPaths(t)
	p := agent
	p.Read = []string{"/run/umbral/shellinteg-1"}
	text, err := Render(p)
	if err != nil {
		t.Fatal(err)
	}
	order(t, text, `(deny file-read* (subpath "/run/umbral"))`, `(allow file-read* (subpath "/run/umbral/shellinteg-1"))`)
	if strings.Contains(text, `(allow file-write* (subpath "/run/umbral/shellinteg-1"))`) {
		t.Errorf("a read entry was given a write rule:\n%s", text)
	}
	if strings.Count(text, "(allow file-read* ") != 3 {
		t.Errorf("read is reopened somewhere unexpected:\n%s", text)
	}
	if _, err := Render(domain.Profile{Read: []string{"/w/\x01"}}); err == nil {
		t.Error("a read entry with a control character was rendered")
	}
}

// TestAPathCannotWriteRulesOfItsOwn_REQ_SBX_001: a path is data. A quote or a backslash in one
// is escaped, so it cannot close its string and add a rule; a control character is refused,
// and so is a path that is not clean and absolute.
func TestAPathCannotWriteRulesOfItsOwn_REQ_SBX_001(t *testing.T) {
	posixPaths(t)
	text, err := Render(domain.Profile{Write: []string{`/w/a")) (allow file-write* (subpath "/x`, `/w/back\slash`}})
	if err != nil {
		t.Fatal(err)
	}
	order(t, text, `(allow file-write* (subpath "/w/a\")) (allow file-write* (subpath \"/x"))`, `(allow file-write* (subpath "/w/back\\slash"))`)
	if strings.Contains(text, `(allow file-write* (subpath "/x"))`) {
		t.Errorf("a path wrote a rule of its own:\n%s", text)
	}
	for name, p := range map[string]domain.Profile{
		"a newline":       {Write: []string{"/w/a\n(allow file-write*)"}},
		"a control byte":  {DenyRead: []string{"/w/\x01"}},
		"a relative path": {Write: []string{"w"}},
		"an unclean path": {DenyRead: []string{"/w/../x"}},
	} {
		if text, err := Render(p); err == nil {
			t.Errorf("%s was rendered:\n%s", name, text)
		}
	}
}

// TestTheCommandRunsThroughAppleTool_REQ_SBX_001: an enforcing adapter runs the command
// through sandbox-exec, named by its full path, with the profile; one that does not enforce
// returns the command as it was, and says why in its status (REQ-SBX-003). A command whose
// name the tool would read as a flag is refused.
func TestTheCommandRunsThroughAppleTool_REQ_SBX_001(t *testing.T) {
	posixPaths(t)
	on := &Sandbox{status: domain.Status{State: domain.StateEnforcing, Mechanism: Mechanism}}
	argv, err := on.Command(agent, []string{"/bin/agent", "acp", "-p"})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := Render(agent)
	if want := []string{"/usr/bin/sandbox-exec", "-p", text, "/bin/agent", "acp", "-p"}; !slices.Equal(argv, want) {
		t.Errorf("Command = %q", argv)
	}
	if _, err := on.Command(agent, []string{"-f", "/etc/profile.sb"}); err == nil {
		t.Error("a command named like a flag of the tool's was accepted")
	}
	if _, err := on.Command(agent, nil); err == nil {
		t.Error("no command was accepted")
	}
	if _, err := on.Command(domain.Profile{Write: []string{"relative"}}, []string{"/bin/agent"}); err == nil {
		t.Error("a profile that cannot be written was accepted")
	}
	off := &Sandbox{status: domain.Status{State: domain.StateUnavailable, Reason: domain.ReasonNoSeatbelt}}
	if argv, err := off.Command(agent, []string{"/bin/agent"}); err != nil || !slices.Equal(argv, []string{"/bin/agent"}) {
		t.Errorf("an adapter that does not enforce returned %q, %v", argv, err)
	}
}

// TestANetworkProfileOpensOnlyItsLoopbackPorts_REQ_SBX_008: a profile that names a network
// refuses all of it, the local sockets and the resolver's included, and then allows the
// loopback's ports it names — a connection out to one kind, a bind and what arrives on the
// other. A profile without one keeps the network open and says nothing of ports.
func TestANetworkProfileOpensOnlyItsLoopbackPorts_REQ_SBX_008(t *testing.T) {
	posixPaths(t)
	p := agent
	p.Net = &domain.Net{Connect: []uint16{4096, 51234}, Bind: []uint16{4096}}
	text, err := Render(p)
	if err != nil {
		t.Fatal(err)
	}
	order(t, text, `(deny network*)`,
		`(allow network-outbound (remote tcp "localhost:4096"))`,
		`(allow network-outbound (remote tcp "localhost:51234"))`,
		`(allow network-bind (local tcp "localhost:4096"))`,
		`(allow network-inbound (local tcp "localhost:4096"))`)
	if strings.Count(text, "(allow network-") != 4 {
		t.Errorf("the profile opens more of the network than its ports:\n%s", text)
	}
	if strings.Contains(text, resolver) || strings.Contains(text, `"localhost:51234"))`+"\n(allow network-bind (local tcp \"localhost:51234\")") {
		t.Errorf("the resolver's socket or a bind on a connect port is open:\n%s", text)
	}
	// An `ip` filter would open the port to a datagram too.
	if strings.Contains(text, " ip \"") {
		t.Errorf("a port is opened to more than TCP:\n%s", text)
	}
	// No service that reaches the network for its caller: a name resolved is a name sent out.
	for _, name := range []string{"dnssd", "SystemConfiguration", "trustd", "ocspd"} {
		if strings.Contains(text, name) {
			t.Errorf("a process without a network may still look up %s:\n%s", name, text)
		}
	}
	if !strings.Contains(text, `(allow mach-lookup (global-name "com.apple.system.logger"))`) {
		t.Errorf("the services that reach no network were dropped too:\n%s", text)
	}
	open, err := Render(agent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(open, "(deny network*)") || strings.Contains(open, "localhost:") || !strings.Contains(open, "com.apple.trustd") {
		t.Errorf("a profile without a network refuses it, or lost its services:\n%s", open)
	}
	if _, err := Render(domain.Profile{Net: &domain.Net{Bind: []uint16{0}}}); err == nil {
		t.Error("a bind on port 0, the kernel's choice of port, was rendered")
	}
}
