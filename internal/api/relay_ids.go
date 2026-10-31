package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	remotedomain "github.com/ecrespo/umbral/internal/remote/domain"
)

// A remote host's ids cross this daemon as `host/id` (REQ-RMT-001, API §5.44): every id a host's
// daemon hands out is prefixed with the host's name on the way to the client, and a request that
// carries such an id is relayed to that host with the prefix taken off. Which members hold an id
// is a rule over their names, so a method this build knows is relayed without a table to update;
// one it does not know is METHOD_NOT_FOUND before any routing.

// hostParam is the member a request names its host with when it carries no id of the host's: a
// `thread.create` or a `workspace.list` for that machine. It is taken off before relaying.
const hostParam = "host"

// issueNotAName is the issue of a `host` member that is not a host's name.
const issueNotAName = "not_a_name"

// issueMixedHosts is the issue of a request whose ids belong to more than one daemon.
const issueMixedHosts = "mixed_hosts"

// workspaceMember is thread.create's `workspace`, the one id member without an `_id` suffix.
const workspaceMember = "workspace"

// isIDKey reports whether a member holds an id the relay rewrites: `id`, `workspace`, and every
// `*_id` and `*_ids`, except the ones that name something of the connection or the model catalog
// rather than of the daemon's state.
func isIDKey(key string) bool {
	switch key {
	case "client_msg_id", "connection_id", "trace_id", "model_id", "provider_id":
		return false
	case "id", workspaceMember:
		return true
	}
	return strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_ids")
}

// decodeLoose reads JSON keeping numbers as written, so a rewrite changes nothing but ids.
func decodeLoose(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// walkIDs calls f on every string held by an id member, at any depth, and stores what it returns.
func walkIDs(v any, f func(string) (string, error)) error {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isIDKey(k) {
				switch s := val.(type) {
				case string:
					out, err := f(s)
					if err != nil {
						return err
					}
					t[k] = out
					continue
				case []any:
					for i, e := range s {
						if str, ok := e.(string); ok {
							out, err := f(str)
							if err != nil {
								return err
							}
							s[i] = out
						}
					}
					continue
				}
			}
			if err := walkIDs(val, f); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range t {
			if err := walkIDs(e, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// routeParams finds the host a request is for and returns its parameters as that host's daemon
// must read them. A request is for a host when its `host` member names one, or when an id member
// holds `host/…` for a host known by that name; it is for this daemon when neither does. Ids of
// two hosts, or of a host and this daemon's `host` member for another, are VALIDATION_ERROR: one
// request reaches one daemon.
func routeParams(raw json.RawMessage, known func(string) bool) (host string, out json.RawMessage, routed bool, err error) {
	if len(raw) == 0 || raw[0] != '{' {
		return "", raw, false, nil
	}
	v, err := decodeLoose(raw)
	if err != nil {
		return "", raw, false, nil //nolint:nilerr // the handler reports malformed parameters in its own words
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return "", raw, false, nil
	}
	if h, present := obj[hostParam]; present {
		name, isString := h.(string)
		if !isString || remotedomain.ValidateName(name) != nil {
			return "", nil, false, ValidationError("host must be a remote host's name", ErrorField{Field: hostParam, Issue: issueNotAName})
		}
		if !known(name) {
			return "", nil, false, fmt.Errorf("%w: %s", remotedomain.ErrNotFound, name)
		}
		host = name
		delete(obj, hostParam)
	}
	byMember := host != ""
	unprefixed := false
	err = walkIDs(obj, func(s string) (string, error) {
		prefix, rest, cut := strings.Cut(s, "/")
		if !cut || !known(prefix) {
			if s != "" {
				unprefixed = true
			}
			return s, nil
		}
		if host != "" && host != prefix {
			return "", ValidationError("a request may name one daemon's ids only", ErrorField{Field: hostParam, Issue: issueMixedHosts})
		}
		host = prefix
		return rest, nil
	})
	if err != nil {
		return "", nil, false, err
	}
	if host == "" {
		return "", raw, false, nil
	}
	// Ids are not unique across daemons (`w1:t1` names a tab on each), so a request routed by one
	// prefixed id may not also carry an unprefixed one: it would name the host's object, not the
	// local one the client holds. Under a `host` member every id is the host's, as written.
	if !byMember && unprefixed {
		return "", nil, false, ValidationError("a request may name one daemon's ids only", ErrorField{Field: hostParam, Issue: issueMixedHosts})
	}
	out, err = json.Marshal(obj)
	return host, out, err == nil, err
}

// prefixIDs rewrites a host's result or notification for the client: every id becomes
// `host/id`, and a workspace's label leads with the host's name, so the client shows where it
// lives without knowing that a relay exists.
func prefixIDs(raw json.RawMessage, host string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	v, err := decodeLoose(raw)
	if err != nil {
		return nil, err
	}
	_ = walkIDs(v, func(s string) (string, error) { return host + "/" + s, nil })
	labelWorkspaces(v, host)
	return json.Marshal(v)
}

// labelWorkspaces prefixes the label of every Workspace (API §4: it alone has `tab_ids`).
func labelWorkspaces(v any, host string) {
	switch t := v.(type) {
	case map[string]any:
		if _, isWorkspace := t["tab_ids"]; isWorkspace {
			if label, ok := t["label"].(string); ok {
				t["label"] = host + ": " + label
			}
		}
		for _, val := range t {
			labelWorkspaces(val, host)
		}
	case []any:
		for _, e := range t {
			labelWorkspaces(e, host)
		}
	}
}
