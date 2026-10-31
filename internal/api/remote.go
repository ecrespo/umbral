package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	remotedomain "github.com/ecrespo/umbral/internal/remote/domain"
	remoteports "github.com/ecrespo/umbral/internal/remote/ports"
)

// capabilityFederation is §2's reserved `federation`: remote.* and the relay (REQ-RMT-001).
const capabilityFederation = "federation"

// notificationRemoteStateChanged is API §6's announcement of a host's link state.
const notificationRemoteStateChanged = "remote.state_changed"

// remoteNameField is the member remote.add and remote.remove name a host with.
const remoteNameField = "name"

// RemoteHost is API Spec §4's RemoteHost.
type RemoteHost struct {
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	State     string  `json:"state"`
	LastError *string `json:"last_error"`
	CreatedAt int64   `json:"created_at"`
}

func toWireRemoteHost(s remoteports.Status) RemoteHost {
	return RemoteHost{
		Name: s.Host.Name, URL: s.Host.URL, State: string(s.State),
		LastError: emptyAsNull(s.LastError), CreatedAt: s.Host.CreatedAt,
	}
}

// remoteStatePayload is `remote.state_changed` (API §6, REQ-RMT-002).
type remoteStatePayload struct {
	Name      string  `json:"name"`
	State     string  `json:"state"`
	LastError *string `json:"last_error"`
}

func hasRemotes(cfg Config) bool { return cfg.Remotes != nil }

// remoteMethods is §5.44. Adding or removing a host changes what this daemon reaches and as
// whom, so an agent's terminal may not (REQ-ORC-004); listing is anyone's.
func remoteMethods() map[string]method {
	return map[string]method{
		"remote.list":   {handle: handleRemoteList, kinds: treeClients, params: emptyResult{}, result: remoteListResult{}},
		"remote.add":    {handle: handleRemoteAdd, userOnly: true, kinds: treeClients, params: remoteAddParams{}, result: RemoteHost{}},
		"remote.remove": {handle: handleRemoteRemove, userOnly: true, kinds: treeClients, params: remoteRemoveParams{}, result: emptyResult{}},
	}
}

type remoteListResult struct {
	Items []RemoteHost `json:"items"`
}

func remotes(c *conn) (remoteports.Hosts, error) {
	if c.server.cfg.Remotes == nil {
		return nil, fmt.Errorf("%w: remote hosts are not wired in", ErrNotImplemented)
	}
	return c.server.cfg.Remotes, nil
}

func handleRemoteList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := remotes(c)
	if err != nil {
		return nil, err
	}
	var p emptyResult
	if err := decode(raw, &p, "remote.list"); err != nil {
		return nil, err
	}
	list, err := svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := remoteListResult{Items: make([]RemoteHost, 0, len(list))}
	for _, s := range list {
		out.Items = append(out.Items, toWireRemoteHost(s))
	}
	return out, nil
}

type remoteAddParams struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func handleRemoteAdd(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := remotes(c)
	if err != nil {
		return nil, err
	}
	var p remoteAddParams
	if err := decode(raw, &p, "remote.add"); err != nil {
		return nil, err
	}
	s, err := svc.Add(ctx, p.Name, p.URL)
	if err != nil {
		return nil, err
	}
	return toWireRemoteHost(s), nil
}

type remoteRemoveParams struct {
	Name string `json:"name"`
}

func handleRemoteRemove(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	svc, err := remotes(c)
	if err != nil {
		return nil, err
	}
	var p remoteRemoveParams
	if err := decode(raw, &p, "remote.remove"); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, ValidationError("name is required", ErrorField{Field: remoteNameField, Issue: requiredTag})
	}
	if err := svc.Remove(ctx, p.Name); err != nil {
		return nil, err
	}
	return emptyResult{}, nil
}

// remoteDomainError maps the remote module's sentinels onto §3. A host whose link is down is
// PROVIDER_UNAVAILABLE: the request was valid and may succeed once the host is back.
func remoteDomainError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, remotedomain.ErrNotFound):
		return codeNotFound, domainNotFound, true
	case errors.Is(err, remotedomain.ErrValidation):
		return codeValidationError, domainValidationError, true
	case errors.Is(err, remotedomain.ErrConflict):
		return codeConflict, domainConflict, true
	case errors.Is(err, remotedomain.ErrUnavailable):
		return codeProviderUnavailable, domainProviderUnavailable, true
	default:
		return 0, "", false
	}
}
