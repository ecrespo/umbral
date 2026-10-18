package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/ecrespo/umbral/internal/bus"
	"github.com/ecrespo/umbral/internal/context/adapters/skills"
	"github.com/ecrespo/umbral/internal/store"
)

// newSkills builds the skill store (T-F1-34) in `skills/` beside the database — which is
// `$XDG_DATA_HOME/umbral/skills` unless `--db` moves both — and runs Data Model §6 step 9
// before any client can install, so an interrupted install from the last run is settled first.
func newSkills(ctx context.Context, db *store.Store, b *bus.Bus, logger *slog.Logger) (*skills.Service, error) {
	svc, err := skills.New(skills.Config{
		DB: db, Dir: filepath.Join(filepath.Dir(db.Path()), "skills"), Bus: b,
		NewID: func() string { return store.NewID(store.PrefixSkill) }, Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	return svc, svc.Recover(ctx)
}
