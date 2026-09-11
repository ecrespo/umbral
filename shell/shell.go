// Package shell carries the shell-integration bootstrap scripts (REQ-BLK-005).
//
// The scripts live here rather than beside the Go code that installs them because they
// are also the artifact a package ships to /usr/share, and because Tech Design §5.1 places
// them at the repository root. This file exists only so `go:embed` can reach them: embed
// paths cannot climb out of their package directory.
package shell

import "embed"

// FS holds the bootstrap script for each supported shell.
//
// The `all:` prefix is what includes zsh/.zshrc: `go:embed` skips names beginning with a
// dot, and zsh will only read the file under that exact name.
//
//go:embed bash/umbral.bash fish/umbral.fish all:zsh
var FS embed.FS
