package usage

import (
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

// statsFileName is the file name used to persist usage statistics under the auth directory.
const statsFileName = "usage_stats.json"

// Configure applies the usage statistics settings derived from cfg.
//
// It toggles in-memory collection and resolves the persistence path so callers
// only need a single hook instead of duplicating the wiring at every call site.
func Configure(cfg *config.Config) {
	if cfg == nil {
		SetStatisticsEnabled(false)
		SetPersistPath("")
		return
	}

	SetStatisticsEnabled(cfg.UsageStatisticsEnabled)
	SetPersistPath(resolveStatsPath(cfg))
}

// resolveStatsPath returns the absolute statistics file path, or an empty string
// when persistence is disabled or no auth directory is configured.
func resolveStatsPath(cfg *config.Config) string {
	if !cfg.UsageStatisticsEnabled || cfg.AuthDir == "" {
		return ""
	}
	resolvedDir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
	if errResolve != nil {
		return filepath.Join(cfg.AuthDir, statsFileName)
	}
	return filepath.Join(resolvedDir, statsFileName)
}

// ConfigChanged reports whether the usage statistics wiring needs to be reapplied.
func ConfigChanged(oldCfg, newCfg *config.Config) bool {
	if oldCfg == nil || newCfg == nil {
		return true
	}
	return oldCfg.UsageStatisticsEnabled != newCfg.UsageStatisticsEnabled ||
		oldCfg.AuthDir != newCfg.AuthDir
}
