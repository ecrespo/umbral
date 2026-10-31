// Command umbral-desktop is the Wails v3 desktop client: a window over `umbrald`'s published
// protocol, holding nothing the daemon does not (REQ-DSK-001, DD-001).
//
// It is a module of its own, so Wails and its GTK and WebKit cgo dependencies reach neither
// the daemon's build nor its gates; it is built in CI and in `packaging/desktop/Dockerfile`
// (delta `2026-10-desktop-client`, decision 1). Everything it does is in `internal/desktop`,
// which the daemon's own suite tests against a real daemon; this package binds it to a page.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/desktop"
	"github.com/ecrespo/umbral/internal/desktop/adapters/vtframe"
)

//go:embed frontend
var assets embed.FS

// version is set at build time with -ldflags "-X main.version=…".
var version = "0.0.0-dev"

// Umbral is what the page calls. Every method is the bridge's, so the page reaches the daemon
// through the bridge's list of methods and nothing else.
type Umbral struct {
	bridge *desktop.Bridge
	app    *application.App
	smoke  string
}

// Call calls a protocol method the bridge allows.
func (u *Umbral) Call(method string, params json.RawMessage) (json.RawMessage, error) {
	return u.bridge.Call(method, params)
}

// Attach draws a session at cols×rows; Detach stops; Input types; Resize resizes.
func (u *Umbral) Attach(sessionID string, cols, rows int) error {
	return u.bridge.Attach(sessionID, cols, rows)
}
func (u *Umbral) Detach(sessionID string) error      { return u.bridge.Detach(sessionID) }
func (u *Umbral) Input(sessionID, data string) error { return u.bridge.Input(sessionID, data) }
func (u *Umbral) Resize(sessionID string, cols, rows int) error {
	return u.bridge.Resize(sessionID, cols, rows)
}

// Threads is the threads view of a workspace (REQ-ORC-003).
func (u *Umbral) Threads(workspaceID string) ([]desktop.ThreadRow, error) {
	return u.bridge.Threads(workspaceID)
}

// Version is the client's version, shown in the window.
func (u *Umbral) Version() string { return version }

// Home is the user's home directory, where a workspace starts when no pane gives one.
func (u *Umbral) Home() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return home
}

// SmokeMarker is the text a smoke run types into the first pane, "" otherwise.
func (u *Umbral) SmokeMarker() string { return u.smoke }

// SmokeDone ends a smoke run: the page saw the marker's output drawn in a pane.
func (u *Umbral) SmokeDone(text string) {
	if u.smoke == "" {
		return
	}
	// The page typed `echo smoke-$((marker+1))`: only the shell's answer has the sum.
	marker, err := strconv.ParseUint(u.smoke, 10, 63)
	if err != nil || !strings.Contains(text, "smoke-"+strconv.FormatUint(marker+1, 10)) {
		log.Printf("smoke: the pane shows %q", text)
		os.Exit(1)
	}
	log.Print("smoke: drawn")
	u.app.Quit()
}

// emitter delivers the bridge's events through Wails' event bus.
type emitter struct{ app *application.App }

func (e emitter) Emit(name string, data any) { e.app.Event.Emit(name, data) }

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	c, err := client.Connect(ctx, client.Options{ClientKind: client.ClientKindDesktop})
	cancel()
	if err != nil {
		log.Fatalf("umbral-desktop: connect to umbrald: %v", err)
	}
	stream := client.NewUnboundedStream(c)

	u := &Umbral{smoke: os.Getenv("UMBRAL_DESKTOP_SMOKE")}
	// The marker is typed into a shell by the page, so it is digits only.
	if strings.Trim(u.smoke, "0123456789") != "" {
		log.Fatalf("umbral-desktop: UMBRAL_DESKTOP_SMOKE is %q, want digits", u.smoke)
	}
	frontend, err := fs.Sub(assets, "frontend")
	if err != nil {
		log.Fatal(err)
	}
	app := application.New(appOptions(u, frontend))
	u.app = app
	// The daemon closes the stream when a newer one takes its sessions over (REQ-HND-001); the
	// window dials it again, without starting one, and follows its terminals there.
	redial := func(ctx context.Context) (*client.Stream, error) {
		c, err := client.Connect(ctx, client.Options{ClientKind: client.ClientKindDesktop, NoAutostart: true})
		if err != nil {
			return nil, err
		}
		return client.NewUnboundedStream(c), nil
	}
	u.bridge = desktop.NewReconnecting(stream, redial, emitter{app}, vtframe.New)
	defer func() { _ = u.bridge.Close() }()

	app.Window.NewWithOptions(windowOptions())
	if u.smoke != "" {
		go func() {
			time.Sleep(90 * time.Second)
			log.Print("smoke: nothing drawn in 90 s")
			os.Exit(1)
		}()
	}
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "umbral-desktop:", err)
		os.Exit(1)
	}
}
