module github.com/ecrespo/umbral/cmd/umbral-desktop

go 1.27.3

require (
	github.com/ecrespo/umbral v0.0.0
	github.com/wailsapp/wails/v3 v3.0.0
)

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	go.mitchellh.com/libghostty v0.0.0-20260908040635-9f448dfe8052 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	mvdan.cc/sh/v3 v3.14.1 // indirect
)

// The client is built from the repository it lives in, never from a published version.
replace github.com/ecrespo/umbral => ../..
