package api

import "github.com/ecrespo/umbral/internal/config"

// securePrivateFile gives the token a protected access list that admits the current user only
// and reads it back: Windows ignores a file mode (delta `2026-10-windows-port`, decision 3).
func securePrivateFile(path string) error {
	return config.ProtectPrivateFile(path)
}
