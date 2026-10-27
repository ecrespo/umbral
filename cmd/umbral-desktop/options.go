package main

import (
	"io/fs"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// AppID is the application's id: the Wayland `app_id`, the X11 `WM_CLASS` and the name of the
// kit's `.desktop` file, which must agree for the desktop to show the icon (REQ-PKG-004).
const AppID = "io.github.ecrespo.Umbral"

// appOptions are the application's options. GTK takes the Wayland `app_id` and, on X11, the
// `WM_CLASS` from the program name, so both are the AppID.
func appOptions(svc *Umbral, frontend fs.FS) application.Options {
	return application.Options{
		Name:        "Umbral",
		Description: "Umbral, a local-first agentic terminal",
		Services:    []application.Service{application.NewService(svc)},
		Assets:      application.AssetOptions{Handler: application.BundledAssetFileServer(frontend)},
		Linux:       application.LinuxOptions{ApplicationID: AppID, ProgramName: AppID},
		Mac:         application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	}
}

// windowOptions is the main window.
func windowOptions() application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name: "main", Title: "Umbral", URL: "/", Width: 1280, Height: 800,
		BackgroundColour: application.NewRGB(0x10, 0x0d, 0x2e),
	}
}
