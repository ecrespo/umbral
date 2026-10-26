package ghostty

import (
	"fmt"

	"go.mitchellh.com/libghostty"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

// ghosttyKey is a domain key name's libghostty name; a letter or a digit is `key_x` or
// `digit_n`.
func ghosttyKey(name string) (libghostty.Key, error) {
	canonical := map[string]string{
		"up": "arrow_up", "down": "arrow_down", "left": "arrow_left", "right": "arrow_right",
	}[name]
	switch {
	case canonical != "":
	case domain.IsCharKey(name) && name[0] >= 'a':
		canonical = "key_" + name
	case domain.IsCharKey(name):
		canonical = "digit_" + name
	default:
		canonical = name
	}
	return libghostty.ParseKey(canonical)
}

// keyText is what a key types when it types anything: the character a letter, digit or space
// stands for. The encoder reads it for an unmodified or shifted key, and for alt's ESC prefix.
func keyText(k domain.Key) string {
	switch {
	case k.Name == "space":
		return " "
	case domain.IsCharKey(k.Name) && !k.Ctrl:
		return k.Name
	}
	return ""
}

// EncodeKey encodes one named key as the terminal's own modes ask — application cursor keys,
// the keypad mode, the Kitty keyboard flags — with libghostty's key encoder (REQ-FTU-001).
func (e *Emulator) EncodeKey(k domain.Key) ([]byte, error) {
	key, err := ghosttyKey(k.Name)
	if err != nil || (!domain.IsCharKey(k.Name) && !isNamed(k.Name)) {
		return nil, fmt.Errorf("%w: %q is not a key", domain.ErrValidation, k.Name)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term == nil {
		return nil, fmt.Errorf("ghostty: encode a key on a closed emulator")
	}
	enc, err := libghostty.NewKeyEncoder()
	if err != nil {
		return nil, fmt.Errorf("ghostty: key encoder: %w", err)
	}
	defer enc.Close()
	enc.SetOptFromTerminal(e.term)
	// An agent's alt is alt on every platform: the macOS option key's own characters are not
	// what it asked for, and the call above resets this.
	enc.SetOptOptionAsAlt(libghostty.OptionAsAltTrue)

	ev, err := libghostty.NewKeyEvent()
	if err != nil {
		return nil, fmt.Errorf("ghostty: key event: %w", err)
	}
	defer ev.Close()
	ev.SetAction(libghostty.KeyActionPress)
	ev.SetKey(key)
	var mods libghostty.Mods
	if k.Ctrl {
		mods |= libghostty.ModCtrl
	}
	if k.Alt {
		mods |= libghostty.ModAlt
	}
	if k.Shift {
		mods |= libghostty.ModShift
	}
	ev.SetMods(mods)
	if text := keyText(k); text != "" {
		ev.SetUTF8(text)
		ev.SetUnshiftedCodepoint(rune(text[0]))
	} else if domain.IsCharKey(k.Name) {
		ev.SetUnshiftedCodepoint(rune(k.Name[0]))
	}
	out, err := enc.Encode(ev)
	if err != nil {
		return nil, fmt.Errorf("ghostty: encode %q: %w", k.Name, err)
	}
	return out, nil
}

func isNamed(name string) bool {
	for _, n := range domain.KeyNames {
		if n == name {
			return true
		}
	}
	return false
}
