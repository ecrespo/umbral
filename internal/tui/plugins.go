package tui

import (
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ecrespo/umbral/internal/tui/ports"
)

// pluginKeysLoadedMsg carries the installed plugins' key bindings (delta `2026-10-plugins`,
// decision 8).
type pluginKeysLoadedMsg struct {
	keys []ports.PluginKey
	err  error
}

func (m *Model) loadPluginKeysCmd() tea.Cmd {
	daemon := m.daemon
	return func() tea.Msg {
		ctx, cancel := withTimeout()
		defer cancel()
		keys, err := daemon.PluginKeys(ctx)
		return pluginKeysLoadedMsg{keys: keys, err: err}
	}
}

// pluginKeysLoaded replaces the bindings. A listing that failed keeps the ones there were.
func (m *Model) pluginKeysLoaded(msg pluginKeysLoadedMsg) {
	if msg.err != nil {
		m.status = "plugins: " + msg.err.Error()
		return
	}
	m.pluginKeys = make(map[string]ports.PluginKey, len(msg.keys))
	for _, k := range msg.keys {
		m.pluginKeys[k.Key] = k
	}
}

// pluginKey types a bound command at the focused pane's prompt without pressing Enter, as a
// restored pane's command is (REQ-TERM-011): the user sees the line and runs it, so it is the
// user's command (REQ-PLG-002). A pane not at a prompt is typed nothing. It reports false for a
// key no plugin binds.
func (m *Model) pluginKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k, ok := m.pluginKeys[msg.String()]
	if !ok {
		return nil, false
	}
	p := m.focusedPane()
	if p == nil || p.exited {
		return nil, true
	}
	// Only at a prompt: a full-screen program or one reading keys would take the letters as
	// commands of its own, which no Enter is needed for.
	if at, err := p.screen.AtPrompt(); err != nil || !at {
		m.status = k.Plugin + ": " + k.Command + " — the pane is not at a prompt; nothing typed"
		return nil, true
	}
	m.status = k.Plugin + ": " + k.Command + " — Enter runs it"
	return m.inputCmd(p.sessionID, []byte(quoteLine(k.Argv))), true
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// quoteLine is argv as a POSIX shell line: a word with anything a shell reads is single-quoted.
// A manifest's words hold no control character, which the plugin store refuses at inspect.
func quoteLine(argv []string) string {
	words := make([]string, 0, len(argv))
	for _, w := range argv {
		if plainWord.MatchString(w) {
			words = append(words, w)
			continue
		}
		words = append(words, "'"+strings.ReplaceAll(w, "'", `'\''`)+"'")
	}
	return strings.Join(words, " ")
}
