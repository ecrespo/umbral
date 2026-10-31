package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/client"
)

// buildFakeSSH builds the ssh stand-in the remote module's tests share.
func buildFakeSSH(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakessh")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./internal/remote/adapters/sshtunnel/fakessh")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakessh: %v\n%s", err, out)
	}
	return bin
}

// remoteSetup is a local daemon and the "machines" it reaches through fakessh: each one a daemon
// of its own, whose every XDG directory sits under root/<host>/.
type remoteSetup struct {
	bin, umb string
	root     string
	pidDir   string
	local    string
	hosts    map[string]string
	// procs and envs are each host's daemon and the environment it was started with.
	procs map[string]*daemonProcess
	envs  map[string][]string
}

// remoteDaemons starts one daemon per host name, then the local daemon configured to reach them
// with fakessh and the real umb.
func remoteDaemons(t *testing.T, hosts ...string) *remoteSetup {
	t.Helper()
	return remoteDaemonsWithModels(t, nil, hosts...)
}

// remoteDaemonsWithModels is remoteDaemons with a models.toml for the hosts models names.
func remoteDaemonsWithModels(t *testing.T, models map[string]string, hosts ...string) *remoteSetup {
	t.Helper()
	s := &remoteSetup{
		bin: buildDaemon(t), umb: buildUmb(t), hosts: map[string]string{},
		procs: map[string]*daemonProcess{}, envs: map[string][]string{},
	}
	ssh := buildFakeSSH(t)
	dir := socketDir(t)
	s.root, s.pidDir = filepath.Join(dir, "hosts"), filepath.Join(dir, "pids")
	if err := os.MkdirAll(s.pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		base := filepath.Join(s.root, h)
		run := filepath.Join(base, "run")
		if err := os.MkdirAll(filepath.Join(run, "umbral"), 0o700); err != nil {
			t.Fatal(err)
		}
		if toml, ok := models[h]; ok {
			if err := os.MkdirAll(filepath.Join(base, "config", "umbral"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(base, "config", "umbral", "models.toml"), toml)
		}
		s.envs[h] = []string{
			"XDG_RUNTIME_DIR=" + run, "XDG_DATA_HOME=" + filepath.Join(base, "data"),
			"XDG_CONFIG_HOME=" + filepath.Join(base, "config"), "XDG_STATE_HOME=" + filepath.Join(base, "state"),
			"XDG_CACHE_HOME=" + filepath.Join(base, "cache"),
		}
		s.procs[h] = runDaemon(t, s.bin, nil, s.envs[h]...)
		s.hosts[h] = filepath.Join(run, "umbral", "umbral.sock")
		_ = waitForSocket(t, s.hosts[h]).Close()
	}

	_, daemonDir := isolatedRuntime(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "umbral")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[remote]\nssh = \"" + ssh + "\"\numb = \"" + s.umb + "\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	s.local = filepath.Join(daemonDir, "umbral.sock")
	runDaemon(t, s.bin, nil, "FAKESSH_ROOT="+s.root, "FAKESSH_PIDDIR="+s.pidDir)
	_ = waitForSocket(t, s.local).Close()
	return s
}

// umbRemote runs the real `umb remote` against the local daemon.
func (s *remoteSetup) umbRemote(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), s.umb, append(append([]string{"remote"}, args...), "--no-autostart")...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("umb remote %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// waitHost waits for remote.list to show the host in state.
func waitHost(ctx context.Context, t *testing.T, s *client.Stream, name, state string) {
	t.Helper()
	for {
		var out struct {
			Items []struct{ Name, State string } `json:"items"`
		}
		if err := s.Call(ctx, "remote.list", map[string]any{}, &out); err != nil {
			t.Fatal(err)
		}
		for _, h := range out.Items {
			if h.Name == name && h.State == state {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s never became %s: %+v", name, state, out.Items)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// remoteWorkspace creates a workspace labelled label on a daemon and returns its pane's session.
func remoteWorkspace(ctx context.Context, t *testing.T, s *client.Stream, label string, extra map[string]any) (workspaceID, sessionID string) {
	t.Helper()
	params := map[string]any{"cwd": t.TempDir(), "label": label}
	for k, v := range extra {
		params[k] = v
	}
	var ws struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
		RootPane struct {
			SessionID string `json:"session_id"`
		} `json:"root_pane"`
	}
	if err := s.Call(ctx, "workspace.create", params, &ws); err != nil {
		t.Fatal(err)
	}
	return ws.Workspace.ID, ws.RootPane.SessionID
}

// typeLine sends a line to a session.
func typeLine(ctx context.Context, t *testing.T, s *client.Stream, sessionID, line string) {
	t.Helper()
	if err := s.Call(ctx, "session.input", map[string]any{
		"session_id": sessionID, "data_b64": base64.StdEncoding.EncodeToString([]byte(line + "\n")),
	}, nil); err != nil {
		t.Fatal(err)
	}
}

// awaitRelayed reads notifications until match accepts one.
func awaitRelayed(ctx context.Context, t *testing.T, s *client.Stream, what string, match func(client.Notification) bool) {
	t.Helper()
	for {
		select {
		case n, ok := <-s.Notifications():
			if !ok {
				t.Fatalf("the stream ended waiting for %s: %v", what, s.Err())
			}
			if match(n) {
				return
			}
		case <-ctx.Done():
			t.Fatalf("no %s", what)
		}
	}
}

// outputHolds matches a session.output of sessionID whose data holds text.
func outputHolds(sessionID, text string, seen *strings.Builder) func(client.Notification) bool {
	return func(n client.Notification) bool {
		if n.Method != "session.output" {
			return false
		}
		var p struct {
			SessionID string `json:"session_id"`
			DataB64   string `json:"data_b64"`
		}
		if json.Unmarshal(n.Params, &p) != nil || p.SessionID != sessionID {
			return false
		}
		data, _ := base64.StdEncoding.DecodeString(p.DataB64)
		seen.Write(data)
		return strings.Contains(seen.String(), text)
	}
}

// TestARemoteHostsWorkspacesAppearPrefixed_REQ_RMT_001: `umb remote add` links a host through a
// forwarded socket, the host's workspaces appear in the local snapshot as `nas: …` with every id
// `nas/…`, and its pane is driven through the local daemon as if it were local.
func TestARemoteHostsWorkspacesAppearPrefixed_REQ_RMT_001(t *testing.T) {
	setup := remoteDaemons(t, "nas")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	nas := interactive(ctx, t, setup.hosts["nas"])
	nasWS, nasSession := remoteWorkspace(ctx, t, nas, "api", nil)
	local := interactive(ctx, t, setup.local)
	ownWS, _ := remoteWorkspace(ctx, t, local, "home", nil)

	if out := setup.umbRemote(t, "add", "nas", "ssh://me@nas"); !strings.Contains(out, "added nas") {
		t.Fatalf("umb remote add: %s", out)
	}
	waitHost(ctx, t, local, "nas", "connected")
	if out := setup.umbRemote(t, "list"); !strings.Contains(out, "nas  ssh://me@nas  connected") {
		t.Fatalf("umb remote list: %s", out)
	}

	// The forwarded socket is this user's alone, and no TCP port was asked of ssh.
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(setup.local), "remotes", "nas.*.sock"))
	if len(matches) != 1 {
		t.Fatalf("forwarded sockets: %v", matches)
	}
	if info, err := os.Stat(matches[0]); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the forwarded socket: %v, %v", info, err)
	}

	var snap struct {
		Workspaces []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"workspaces"`
		Panes []struct {
			SessionID string `json:"session_id"`
		} `json:"panes"`
	}
	if err := local.Call(ctx, "session.snapshot", map[string]any{}, &snap); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, w := range snap.Workspaces {
		labels[w.ID] = w.Label
	}
	if labels["nas/"+nasWS] != "nas: api" || labels[ownWS] != "home" {
		t.Fatalf("snapshot workspaces %v, want nas/%s as \"nas: api\" beside %s", labels, nasWS, ownWS)
	}
	remoteSession := "nas/" + nasSession
	found := false
	for _, p := range snap.Panes {
		found = found || p.SessionID == remoteSession
	}
	if !found {
		t.Fatalf("the host's pane %s is not in the snapshot", remoteSession)
	}

	if err := local.Call(ctx, "session.subscribe", map[string]any{"session_id": remoteSession}, nil); err != nil {
		t.Fatal(err)
	}
	typeLine(ctx, t, local, remoteSession, "echo relayed-$((6*7))")
	var seen strings.Builder
	awaitRelayed(ctx, t, local, "the host's output", outputHolds(remoteSession, "relayed-42", &seen))

	// An id of a host that is not configured is this daemon's to judge, and it is not one.
	err := local.Call(ctx, "pane.get", map[string]any{"pane_id": "ghost/w1:p1"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not a pane identifier") {
		t.Fatalf("an unknown host's id = %v, want this daemon's refusal", err)
	}

	setup.umbRemote(t, "remove", "nas")
	if out := setup.umbRemote(t, "list"); !strings.Contains(out, "no remote hosts") {
		t.Fatalf("after remove: %s", out)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(setup.local), "remotes", "*.sock")); len(left) != 0 {
		t.Fatalf("a removed host's socket is left: %v", left)
	}
}

// TestARemoteThreadLivesOnItsHost_REQ_RMT_003: a thread created through the local daemon for a
// host is that host's — it is in the host's database and list, and not in the local one's.
func TestARemoteThreadLivesOnItsHost_REQ_RMT_003(t *testing.T) {
	setup := remoteDaemons(t, "nas")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	nas := interactive(ctx, t, setup.hosts["nas"])
	local := interactive(ctx, t, setup.local)
	setup.umbRemote(t, "add", "nas", "ssh://nas")
	waitHost(ctx, t, local, "nas", "connected")

	var created struct {
		ID string `json:"id"`
	}
	if err := local.Call(ctx, "thread.create", map[string]any{"host": "nas", "cwd": "/", "title": "remote work"}, &created); err != nil {
		t.Fatal(err)
	}
	id, onHost := strings.CutPrefix(created.ID, "nas/")
	if !onHost {
		t.Fatalf("thread.create through the relay = %q, want a nas/ id", created.ID)
	}
	var got struct {
		Title string `json:"title"`
	}
	if err := nas.Call(ctx, "thread.get", map[string]any{"thread_id": id}, &got); err != nil || got.Title != "remote work" {
		t.Fatalf("the host's thread.get: %+v, %v", got, err)
	}
	if err := local.Call(ctx, "thread.get", map[string]any{"thread_id": created.ID}, &got); err != nil {
		t.Fatalf("the relayed thread.get: %v", err)
	}

	// The local daemon's own list, asked without a host, does not hold it.
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := local.Call(ctx, "thread.list", map[string]any{}, &list); err != nil {
		t.Fatal(err)
	}
	for _, th := range list.Items {
		if strings.HasSuffix(th.ID, id) && !strings.HasPrefix(th.ID, "nas/") {
			t.Fatalf("the local daemon holds the host's thread: %+v", list.Items)
		}
	}
}

// TestADroppedRemoteLinkComesBackAndItsPanesResume_REQ_RMT_002: killing nas's ssh leaves its
// session running on nas, unsubscribes the local client from it as `remote_disconnected`, shows
// nas `disconnected` while box keeps working, and nas comes back by itself, where subscribing
// again resumes the pane from a snapshot.
func TestADroppedRemoteLinkComesBackAndItsPanesResume_REQ_RMT_002(t *testing.T) {
	setup := remoteDaemons(t, "nas", "box")
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	nas := interactive(ctx, t, setup.hosts["nas"])
	box := interactive(ctx, t, setup.hosts["box"])
	_, nasSession := remoteWorkspace(ctx, t, nas, "n", nil)
	_, boxSession := remoteWorkspace(ctx, t, box, "b", nil)
	local := interactive(ctx, t, setup.local)
	setup.umbRemote(t, "add", "nas", "ssh://nas")
	setup.umbRemote(t, "add", "box", "ssh://box")
	waitHost(ctx, t, local, "nas", "connected")
	waitHost(ctx, t, local, "box", "connected")
	remoteNas, remoteBox := "nas/"+nasSession, "box/"+boxSession
	for _, id := range []string{remoteNas, remoteBox} {
		if err := local.Call(ctx, "session.subscribe", map[string]any{"session_id": id}, nil); err != nil {
			t.Fatal(err)
		}
	}
	typeLine(ctx, t, local, remoteNas, "export MARK=kept-across-the-drop")

	// The link to nas dies as a network would kill it.
	pids, _ := filepath.Glob(filepath.Join(setup.pidDir, "nas.*"))
	if len(pids) != 1 {
		t.Fatalf("nas's forwards: %v", pids)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(filepath.Ext(pids[0]), "."))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(pids[0])

	awaitRelayed(ctx, t, local, "nas's unsubscription", func(n client.Notification) bool {
		var p struct {
			SessionID string `json:"session_id"`
			Reason    string `json:"reason"`
		}
		if n.Method != "session.unsubscribed" || json.Unmarshal(n.Params, &p) != nil {
			return false
		}
		return p.SessionID == remoteNas && p.Reason == "remote_disconnected"
	})
	awaitRelayed(ctx, t, local, "nas disconnected", func(n client.Notification) bool {
		return n.Method == "remote.state_changed" && strings.Contains(string(n.Params), `"name":"nas"`) &&
			strings.Contains(string(n.Params), `"state":"disconnected"`)
	})

	// box is not nas: its pane still answers through the relay.
	typeLine(ctx, t, local, remoteBox, "echo box-$((2+3))")
	var boxSeen strings.Builder
	awaitRelayed(ctx, t, local, "box's output", outputHolds(remoteBox, "box-5", &boxSeen))

	// nas's session lives on, on nas.
	var sessions struct {
		Items []sessionRow `json:"items"`
	}
	if err := nas.Call(ctx, "session.list", map[string]any{}, &sessions); err != nil {
		t.Fatal(err)
	}
	alive := false
	for _, row := range sessions.Items {
		alive = alive || row.ID == nasSession && row.State == "alive"
	}
	if !alive {
		t.Fatalf("nas's session did not survive the drop: %+v", sessions.Items)
	}

	waitHost(ctx, t, local, "nas", "connected")
	var resumed struct {
		Snapshot struct {
			DataB64 string `json:"data_b64"`
		} `json:"snapshot"`
	}
	if err := local.Call(ctx, "session.subscribe", map[string]any{"session_id": remoteNas}, &resumed); err != nil {
		t.Fatal(err)
	}
	typeLine(ctx, t, local, remoteNas, "echo $MARK")
	var nasSeen strings.Builder
	awaitRelayed(ctx, t, local, "nas's output after the drop", outputHolds(remoteNas, "kept-across-the-drop\r", &nasSeen))
}

// TestAHostDaemonRestartBehindALiveLinkIsFollowed_REQ_RMT_002: nas's daemon restarts while its
// ssh forward stays up. The remote module sees no drop, so the relay itself tells the subscribed
// client: its pane is unsubscribed as `remote_disconnected`, and `remote.state_changed`
// `connected` follows once nas answers again, which is what a client resubscribes on.
func TestAHostDaemonRestartBehindALiveLinkIsFollowed_REQ_RMT_002(t *testing.T) {
	setup := remoteDaemons(t, "nas")
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	nas := interactive(ctx, t, setup.hosts["nas"])
	_, nasSession := remoteWorkspace(ctx, t, nas, "n", nil)
	local := interactive(ctx, t, setup.local)
	setup.umbRemote(t, "add", "nas", "ssh://nas")
	waitHost(ctx, t, local, "nas", "connected")
	remoteNas := "nas/" + nasSession
	if err := local.Call(ctx, "session.subscribe", map[string]any{"session_id": remoteNas}, nil); err != nil {
		t.Fatal(err)
	}
	forwards, _ := filepath.Glob(filepath.Join(setup.pidDir, "nas.*"))

	_ = setup.procs["nas"].cmd.Process.Signal(os.Interrupt)
	setup.procs["nas"].exit(t, 15*time.Second)
	awaitRelayed(ctx, t, local, "nas's unsubscription", func(n client.Notification) bool {
		var p struct {
			SessionID string `json:"session_id"`
			Reason    string `json:"reason"`
		}
		if n.Method != "session.unsubscribed" || json.Unmarshal(n.Params, &p) != nil {
			return false
		}
		return p.SessionID == remoteNas && p.Reason == "remote_disconnected"
	})
	setup.procs["nas"] = runDaemon(t, setup.bin, nil, setup.envs["nas"]...)
	awaitRelayed(ctx, t, local, "nas connected again", func(n client.Notification) bool {
		return n.Method == "remote.state_changed" && strings.Contains(string(n.Params), `"name":"nas"`) &&
			strings.Contains(string(n.Params), `"state":"connected"`)
	})
	if again, _ := filepath.Glob(filepath.Join(setup.pidDir, "nas.*")); len(again) != 1 || len(forwards) != 1 || again[0] != forwards[0] {
		t.Fatalf("the forward was replaced (%v, then %v): this test is about a link that stayed up", forwards, again)
	}

	// The restarted nas is reached through the same link: its restored workspace is listed.
	var snap struct {
		Workspaces []struct {
			Label string `json:"label"`
		} `json:"workspaces"`
	}
	if err := local.Call(ctx, "session.snapshot", map[string]any{}, &snap); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range snap.Workspaces {
		found = found || w.Label == "nas: n"
	}
	if !found {
		t.Fatalf("the restarted nas's workspace is not in the snapshot: %+v", snap.Workspaces)
	}
}

// TestARemoteTurnsApprovalStaysOnItsHost_REQ_RMT_003: a turn sent through the local daemon runs
// on nas, against nas's model; its approval reaches the local client as `nas/…` and is answered
// through the relay, and nas alone records the approval and the write.
func TestARemoteTurnsApprovalStaysOnItsHost_REQ_RMT_003(t *testing.T) {
	ollama := &scriptedOllama{replies: [][]string{
		{`{"model":"m","message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"write_file","arguments":{"path":"out.txt","content":"written on nas\n"}}}]},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`},
		{`{"model":"m","message":{"role":"assistant","content":"Done."},"done":true,"done_reason":"stop","prompt_eval_count":20,"eval_count":2}`},
	}}
	srv := ollama.server(t)
	setup := remoteDaemonsWithModels(t, map[string]string{"nas": fmt.Sprintf(`
[classes]
code = ["ollama/m"]
fast = ["ollama/m"]

[[providers]]
id = "ollama"
type = "ollama"
base_url = %q
`, srv.URL)}, "nas")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	nas := interactive(ctx, t, setup.hosts["nas"])
	for {
		var list struct{ Items []struct{ ID, Health string } }
		if err := nas.Call(ctx, "model.list", nil, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) == 1 && list.Items[0].Health == "ok" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("nas never discovered its model: %+v", list)
		case <-time.After(50 * time.Millisecond):
		}
	}
	local := interactive(ctx, t, setup.local)
	setup.umbRemote(t, "add", "nas", "ssh://nas")
	waitHost(ctx, t, local, "nas", "connected")

	cwd := t.TempDir()
	var thread struct{ ID string }
	if err := local.Call(ctx, "thread.create", map[string]any{"host": "nas", "cwd": cwd}, &thread); err != nil {
		t.Fatal(err)
	}
	if err := local.Call(ctx, "thread.send", map[string]any{"thread_id": thread.ID, "text": "write the file"}, nil); err != nil {
		t.Fatal(err)
	}
	var approval struct{ ID, Tool string }
wait:
	for {
		select {
		case n, ok := <-local.Notifications():
			if !ok {
				t.Fatalf("the stream ended: %v", local.Err())
			}
			switch n.Method {
			case "approval.requested":
				_ = n.Decode(&approval)
				if !strings.HasPrefix(approval.ID, "nas/") || approval.Tool != "write_file" {
					t.Fatalf("the relayed approval.requested %+v, want a nas/ write_file", approval)
				}
				if err := local.Call(ctx, "approval.respond", map[string]any{"approval_id": approval.ID, "decision": "approve", "scope": "once"}, nil); err != nil {
					t.Fatal(err)
				}
			case "thread.turn_finished":
				if strings.Contains(string(n.Params), thread.ID) {
					break wait
				}
			}
		case <-ctx.Done():
			t.Fatal("the remote turn did not finish")
		}
	}
	if data, err := os.ReadFile(filepath.Join(cwd, "out.txt")); err != nil || string(data) != "written on nas\n" {
		t.Fatalf("out.txt = %q %v", data, err)
	}

	type approvals struct {
		Items []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"items"`
	}
	var onNas, here approvals
	if err := nas.Call(ctx, "approval.list", map[string]any{"all": true}, &onNas); err != nil {
		t.Fatal(err)
	}
	if len(onNas.Items) != 1 || "nas/"+onNas.Items[0].ID != approval.ID || onNas.Items[0].State != "approved" {
		t.Fatalf("nas's approvals %+v, want the one answered through the relay", onNas.Items)
	}
	if err := local.Call(ctx, "approval.list", map[string]any{"all": true}, &here); err != nil {
		t.Fatal(err)
	}
	if len(here.Items) != 0 {
		t.Fatalf("the local daemon holds approvals %+v: the host's stay on the host", here.Items)
	}
	ollama.mu.Lock()
	calls := len(ollama.bodies)
	ollama.mu.Unlock()
	if calls != 2 {
		t.Fatalf("nas's model was called %d times, want the turn's 2", calls)
	}
}
