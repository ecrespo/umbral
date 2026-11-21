//go:build unix

package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/acp/domain"
	"github.com/ecrespo/umbral/internal/acp/ports"
)

// fakeRelays stands for the daemon's relays: it records what it was asked to start and hands
// back a relay whose Close it counts.
type fakeRelays struct {
	upstream map[string]string

	mu      sync.Mutex
	started []fakeStart
	relays  []*fakeRelay
}

type fakeStart struct {
	thread, provider string
	port             uint16
}

func (f *fakeRelays) Upstream(provider string) (string, error) {
	u, ok := f.upstream[provider]
	if !ok {
		return "", fmt.Errorf("provider %q is not in models.toml", provider)
	}
	return u, nil
}

func (f *fakeRelays) Start(_ context.Context, threadID, provider string, port uint16) (ports.Relay, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, fakeStart{threadID, provider, port})
	r := &fakeRelay{origin: "http://127.0.0.1:" + strconv.Itoa(int(port)), token: "tok-" + threadID}
	f.relays = append(f.relays, r)
	return r, nil
}

func (f *fakeRelays) last(t *testing.T) (fakeStart, *fakeRelay) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.relays) == 0 {
		t.Fatal("no relay was started")
	}
	return f.started[len(f.started)-1], f.relays[len(f.relays)-1]
}

type fakeRelay struct {
	origin, token string
	closed        atomic.Int32
}

func (r *fakeRelay) Origin() string { return r.origin }
func (r *fakeRelay) Token() string  { return r.token }
func (r *fakeRelay) Close() error   { r.closed.Add(1); return nil }

// relayHost is a host whose fake agent is an OpenCode entry behind a relay to `ollama`.
func relayHost(t *testing.T, c *confiner, profiles *[]bool) (*Host, *fakeRelays) {
	t.Helper()
	relays := &fakeRelays{upstream: map[string]string{"ollama": "http://127.0.0.1:11434"}}
	var mu sync.Mutex
	h := newHost(t, func(cfg *Config) {
		cfg.Entries[0].Relay = "ollama"
		cfg.Entries[0].Config = map[string]any{
			"model": "ollama/gpt-oss:20b",
			"provider": map[string]any{"ollama": map[string]any{
				"npm": "@ai-sdk/openai-compatible", "options": map[string]any{"baseURL": "http://127.0.0.1:11434/v1"},
			}},
		}
		cfg.Relays = relays
		if c != nil {
			cfg.Confiner = c
		}
		cfg.NetworkProfile = func(_ context.Context, _ string, confined bool) error {
			mu.Lock()
			defer mu.Unlock()
			*profiles = append(*profiles, confined)
			return nil
		}
	})
	return h, relays
}

// optionsIn is the generated configuration's options of the ollama provider.
func optionsIn(t *testing.T, config string) map[string]any {
	t.Helper()
	var cfg struct {
		Provider map[string]struct {
			Options map[string]any `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		t.Fatalf("the agent's configuration %q: %v", config, err)
	}
	return cfg.Provider["ollama"].Options
}

// TestAHostedAgentIsPointedAtItsRelay_REQ_ACP_009: an entry with a relay gets one started on a
// loopback port of its own, its provider's endpoint replaced by the relay's with the relay's
// token as its key, a second port named by the manifest's flag, and — where the sandbox
// confines the network — a profile of exactly those two ports; the thread is said confined.
func TestAHostedAgentIsPointedAtItsRelay_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	c := &confiner{network: true}
	var profiles []bool
	h, relays := relayHost(t, c, &profiles)
	r := report(t, h, workDir(t))
	start, relay := relays.last(t)
	if start.thread != "thr_1" || start.provider != "ollama" || start.port == 0 {
		t.Fatalf("the relay was started as %+v", start)
	}
	opts := optionsIn(t, r.Config)
	if opts["baseURL"] != relay.origin+"/v1" || opts["apiKey"] != relay.token {
		t.Errorf("the agent's provider options are %v, want the relay's %s/v1 and its token", opts, relay.origin)
	}
	i := slices.Index(r.Args, "--port")
	if i < 0 || i+1 >= len(r.Args) {
		t.Fatalf("the agent was given no port: args %q", r.Args)
	}
	own, err := strconv.ParseUint(r.Args[i+1], 10, 16)
	if err != nil || own == 0 || uint16(own) == start.port {
		t.Fatalf("the agent's port is %q, the relay's %d", r.Args[i+1], start.port)
	}
	want := &domain.Net{Connect: []uint16{start.port, uint16(own)}, Bind: []uint16{uint16(own)}}
	if len(c.got) != 1 || c.got[0].Net == nil || !slices.Equal(c.got[0].Net.Connect, want.Connect) || !slices.Equal(c.got[0].Net.Bind, want.Bind) {
		t.Errorf("the sandbox was asked %+v, want the network %+v", c.got, want)
	}
	if !slices.Equal(profiles, []bool{true}) {
		t.Errorf("the thread's network profile was recorded %v, want [confined]", profiles)
	}
}

// TestARelayWithoutANetworkSandboxLeavesTheThreadOpen_REQ_ACP_007: where the sandbox does not
// confine the network the agent still goes through its relay, but it is given no network
// profile, and its thread is not said confined.
func TestARelayWithoutANetworkSandboxLeavesTheThreadOpen_REQ_ACP_007(t *testing.T) {
	t.Parallel()
	c := &confiner{network: false}
	var profiles []bool
	h, relays := relayHost(t, c, &profiles)
	r := report(t, h, workDir(t))
	_, relay := relays.last(t)
	if optionsIn(t, r.Config)["apiKey"] != relay.token {
		t.Error("the agent was not pointed at its relay")
	}
	if len(c.got) != 1 || c.got[0].Net != nil {
		t.Errorf("a network the sandbox cannot confine was asked for: %+v", c.got)
	}
	if !slices.Equal(profiles, []bool{false}) {
		t.Errorf("the thread's network profile was recorded %v, want [open]", profiles)
	}

	// No relay: no port, no network, and the thread open.
	var plain []bool
	h2 := newHost(t, func(cfg *Config) {
		cfg.Confiner = &confiner{network: true}
		cfg.NetworkProfile = func(_ context.Context, _ string, confined bool) error { plain = append(plain, confined); return nil }
	})
	if r := report(t, h2, workDir(t)); slices.Contains(r.Args, "--port") {
		t.Errorf("an agent without a relay was given a port: %q", r.Args)
	}
	if !slices.Equal(plain, []bool{false}) {
		t.Errorf("a thread without a relay was recorded %v, want [open]", plain)
	}
}

// TestTheRelayEndsWithItsAgent_REQ_SBX_009: a relay never outlives the process it served,
// whether the agent is closed or exits on its own.
func TestTheRelayEndsWithItsAgent_REQ_SBX_009(t *testing.T) {
	t.Parallel()
	var profiles []bool
	h, relays := relayHost(t, nil, &profiles)
	dir := workDir(t)
	report(t, h, dir)
	_, first := relays.last(t)
	h.Close("thr_1")
	if first.closed.Load() != 1 {
		t.Errorf("the relay was closed %d times after its agent was, want 1", first.closed.Load())
	}

	report(t, h, dir)
	_, second := relays.last(t)
	if second == first {
		t.Fatal("the relaunch reused the closed relay")
	}
	// The agent dies between turns, with no turn to notice it and kill what is left.
	pids := h.AgentPIDs()
	if len(pids) != 1 {
		t.Fatalf("agent pids %v, want one", pids)
	}
	proc, err := os.FindProcess(pids[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for second.closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if second.closed.Load() != 1 {
		t.Errorf("the relay was closed %d times after its agent exited, want 1", second.closed.Load())
	}
}

// TestAFailedLaunchClosesItsRelay_REQ_SBX_009: a launch that fails after its relay started —
// the sandbox refusing the agent, or the thread's network profile not recorded — closes that
// relay, or it would listen for the life of the daemon with nothing behind it.
func TestAFailedLaunchClosesItsRelay_REQ_SBX_009(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		confiner *confiner
		profile  error
	}{
		"the sandbox refuses":       {confiner: &confiner{network: true, fail: errors.New("refused")}},
		"the profile is unrecorded": {confiner: &confiner{network: true}, profile: errors.New("disk full")},
	} {
		var profiles []bool
		h, relays := relayHost(t, c.confiner, &profiles)
		if c.profile != nil {
			h.cfg.NetworkProfile = func(context.Context, string, bool) error { return c.profile }
		}
		if _, _, err := prompt(t, h, workDir(t), "report"); err == nil {
			t.Errorf("%s: the launch succeeded", name)
			continue
		}
		_, r := relays.last(t)
		if got := r.closed.Load(); got != 1 {
			t.Errorf("%s: the relay was closed %d times, want 1", name, got)
		}
	}
}

// TestARelayEntryIsRefusedBeforeLaunch_REQ_ACP_009: an entry the relay cannot serve is refused
// by Check, with `data.reason = relay`, before anything is written or started.
func TestARelayEntryIsRefusedBeforeLaunch_REQ_ACP_009(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Config){
		"no relays":      func(c *Config) { c.Relays = nil },
		"an unknown one": func(c *Config) { c.Entries[0].Relay = "nope" },
		"another endpoint": func(c *Config) {
			c.Relays = &fakeRelays{upstream: map[string]string{"ollama": "http://127.0.0.1:9999"}}
		},
	} {
		var profiles []bool
		h, _ := relayHost(t, nil, &profiles)
		mutate(&h.cfg)
		h.entries = map[string]domain.Entry{}
		for _, e := range h.cfg.Entries {
			h.entries[e.Name] = e
		}
		err := h.Check("fake", workDir(t))
		var r *domain.Refusal
		if !errors.As(err, &r) || r.Reason != domain.ReasonRelay {
			t.Errorf("%s: Check = %v, want a %q refusal", name, err, domain.ReasonRelay)
		}
		if f, ok := h.cfg.Relays.(*fakeRelays); ok && len(f.started) != 0 {
			t.Errorf("%s: a relay was started for a refused entry", name)
		}
	}
}
