package pty

import (
	"fmt"
	"os"

	"github.com/ecrespo/umbral/internal/sessions/domain"
)

func validateDir(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("%w: cwd %s: %w", domain.ErrValidation, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: cwd %s is not a directory", domain.ErrValidation, dir)
	}
	return nil
}
