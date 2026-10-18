// Package rules loads the rule material the security module applies: the override directory
// of REQ-SEC-010, layered over the rules in force, and the opt-in remote fetch of REQ-SEC-011.
package rules

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ecrespo/umbral/internal/security/domain"
)

// MaxRuleFileBytes bounds one file of the override directory.
const MaxRuleFileBytes = 1 << 20

// LoadDir reads `*.json` in dir, in name order, and returns their rules layered in that
// order, so a later file wins over an earlier one. Only regular files are read. A file that
// cannot be read or parsed is skipped with a warning naming it (REQ-SEC-010); a missing
// directory is no overrides.
func LoadDir(logger *slog.Logger, dir string) domain.RuleSet {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("the rule override directory cannot be read; the rules in force stay",
				slog.String("dir", dir), slog.Any("error", err))
		}
		return domain.RuleSet{}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)

	var layers []domain.RuleSet
	for _, name := range names {
		path := filepath.Join(dir, name)
		doc, err := readRuleFile(path)
		if err != nil {
			logger.Warn("a rule override file is invalid and was ignored",
				slog.String("file", path), slog.Any("error", err))
			continue
		}
		layers = append(layers, doc.Rules)
		logger.Info("rule override file loaded", slog.String("file", path),
			slog.Int("redaction", len(doc.Rules.Redaction)), slog.Int("destructive", len(doc.Rules.Destructive)))
	}
	return domain.Layer(domain.RuleSet{}, layers...)
}

func readRuleFile(path string) (domain.RuleDocument, error) {
	f, err := os.Open(path) // #nosec G304 -- a file of the user's own override directory
	if err != nil {
		return domain.RuleDocument{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxRuleFileBytes+1))
	if err != nil {
		return domain.RuleDocument{}, err
	}
	if len(data) > MaxRuleFileBytes {
		return domain.RuleDocument{}, fmt.Errorf("%w: over %d bytes", domain.ErrInvalidRules, MaxRuleFileBytes)
	}
	return domain.ParseRules(data)
}
