// Package domain is the domain layer of the remote module, which reaches other machines'
// daemons over SSH and lets the local one relay their protocol (REQ-RMT-001…003). It imports
// nothing outside the standard library (Art. 3).
package domain

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Errors the module returns; internal/api maps them onto API §3.
var (
	ErrValidation = errors.New("remote: invalid request")
	ErrNotFound   = errors.New("remote: no such host")
	ErrConflict   = errors.New("remote: a host already has this name")
	// ErrUnavailable is a request for a host whose link is down (REQ-RMT-002).
	ErrUnavailable = errors.New("remote: the host is not connected")
)

// State is a host's link state (API §4 RemoteHost).
type State string

// States.
const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateDisconnected State = "disconnected"
)

// Host is one configured remote (`remotes`, Data Model §2.14).
type Host struct {
	Name      string
	URL       string
	CreatedAt int64
}

// Target is where ssh connects: a URL's user, host and port, each checked so it reaches ssh as
// an argument and never as an option.
type Target struct {
	User string
	Host string
	// Port is 0 when the URL names none, which leaves ssh's own configuration in charge.
	Port int
}

// Destination is ssh's `[user@]host` for the target.
func (t Target) Destination() string {
	if t.User == "" {
		return t.Host
	}
	return t.User + "@" + t.Host
}

// Endpoint is what a remote daemon answers `umb remote endpoint` with: its socket and the
// token a client presents to it.
type Endpoint struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
}

// MaxNameLen bounds a host name: it is a prefix of every id the host hands out and a path
// component of the forwarded socket, whose whole path the platform caps.
const MaxNameLen = 32

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// A user or host reaches ssh's command line; nothing that starts with a dash, and nothing
	// a shell or ssh's own parser would read twice.
	userPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
)

// ValidateName checks a host name: lowercase, starting with a letter, letters, digits and
// dashes, at most MaxNameLen. It holds no `/`, which separates it from the remote id it
// prefixes.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrValidation)
	}
	if len(name) > MaxNameLen || !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name must be at most %d lowercase letters, digits and dashes, starting with a letter", ErrValidation, MaxNameLen)
	}
	return nil
}

// ParseURL reads `ssh://[user@]host[:port]`. Only ssh is accepted: a remote daemon is reached by
// forwarding its socket, never through a TCP port of its own (REQ-RMT-001).
func ParseURL(raw string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ssh" || u.Opaque != "" {
		return Target{}, fmt.Errorf("%w: url must be ssh://[user@]host[:port]", ErrValidation)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return Target{}, fmt.Errorf("%w: url must be ssh://[user@]host[:port], with no path or query", ErrValidation)
	}
	var t Target
	if u.User != nil {
		if _, has := u.User.Password(); has {
			return Target{}, fmt.Errorf("%w: url may not carry a password", ErrValidation)
		}
		t.User = u.User.Username()
		if !userPattern.MatchString(t.User) {
			return Target{}, fmt.Errorf("%w: url's user is not a plain user name", ErrValidation)
		}
	}
	t.Host = u.Hostname()
	if t.Host == "" {
		return Target{}, fmt.Errorf("%w: url names no host", ErrValidation)
	}
	if net.ParseIP(t.Host) == nil && !hostPattern.MatchString(t.Host) {
		return Target{}, fmt.Errorf("%w: url's host is not a plain host name", ErrValidation)
	}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return Target{}, fmt.Errorf("%w: url's port must be 1 to 65535", ErrValidation)
		}
		t.Port = port
	} else if strings.HasSuffix(u.Host, ":") {
		return Target{}, fmt.Errorf("%w: url's port is empty", ErrValidation)
	}
	return t, nil
}

// ValidateEndpoint checks what the remote side printed before any of it reaches ssh's `-L`.
func ValidateEndpoint(ep Endpoint) error {
	if !strings.HasPrefix(ep.Socket, "/") || strings.ContainsAny(ep.Socket, ":\n\r\x00") {
		return fmt.Errorf("%w: the remote daemon's socket is not an absolute path ssh can forward", ErrValidation)
	}
	if ep.Token == "" {
		return fmt.Errorf("%w: the remote daemon gave no token", ErrValidation)
	}
	return nil
}

// Backoff is the wait before reconnect attempt n (from 0): 1, 2, 4, 8, 16 s, then 30 s
// (REQ-RMT-002).
func Backoff(attempt int) time.Duration {
	if attempt >= 5 {
		return 30 * time.Second
	}
	return time.Duration(1<<attempt) * time.Second
}
