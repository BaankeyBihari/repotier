package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const DefaultPort = 45910

var SupportedEditorIDs = []string{"agy", "vscode", "windsurf", "intellij", "subl"}

type CachedRepo struct {
	Path     string   `json:"path"`
	Name     string   `json:"name,omitempty"`
	Exists   bool     `json:"exists"`
	LastSeen string   `json:"last_seen"`
	Remotes  []Remote `json:"remotes,omitempty"`
}

type RepoCacheFile struct {
	Repos []CachedRepo `json:"repos"`
}

type LoggingConfig struct {
	Level      string `json:"level"`
	MaxSizeMB  int    `json:"max_size_mb"`
	MaxBackups int    `json:"max_backups"`
}

type Config struct {
	Port            int               `json:"port"`
	EditorOverrides map[string]string `json:"editor_overrides"`
	Logging         LoggingConfig     `json:"logging"`
}

type Store struct {
	CacheDir  string
	ConfigDir string
	GOOS      string
	HomeDir   string
	mu        sync.Mutex
}

func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Store{
		CacheDir:  filepath.Join(home, ".cache", "repotier"),
		ConfigDir: filepath.Join(home, ".config", "repotier"),
		GOOS:      runtime.GOOS,
		HomeDir:   home,
	}, nil
}

func NewStore(cacheDir, configDir string) *Store {
	home, _ := os.UserHomeDir()
	return &Store{
		CacheDir:  cacheDir,
		ConfigDir: configDir,
		GOOS:      runtime.GOOS,
		HomeDir:   home,
	}
}

// @spec STORAGE-LOCK-001
func (s *Store) withLock(lockFile string, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(lockFile), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	return fn()
}

// @spec STORAGE-LOCK-001
func writeJSONAtomic(targetPath string, v any) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := targetPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, targetPath)
}

// @spec STORAGE-LOCK-002
func (s *Store) readCacheUnlocked() (RepoCacheFile, error) {
	p := filepath.Join(s.CacheDir, "repos.json")
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RepoCacheFile{Repos: []CachedRepo{}}, nil
		}
		return RepoCacheFile{}, err
	}
	var cf RepoCacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		_ = os.Rename(p, p+".corrupt")
		return RepoCacheFile{Repos: []CachedRepo{}}, nil
	}
	if cf.Repos == nil {
		cf.Repos = []CachedRepo{}
	}
	return cf, nil
}

// @spec STORAGE-CACHE-001
func (s *Store) UpsertRepo(path string) error {
	clean := filepath.Clean(path)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	lockPath := filepath.Join(s.CacheDir, "repos.lock")
	cachePath := filepath.Join(s.CacheDir, "repos.json")

	return s.withLock(lockPath, func() error {
		cf, err := s.readCacheUnlocked()
		if err != nil {
			return err
		}
		found := false
		for i := range cf.Repos {
			if cf.Repos[i].Path == clean {
				cf.Repos[i].LastSeen = now
				found = true
				break
			}
		}
		if !found {
			cf.Repos = append(cf.Repos, CachedRepo{
				Path:     clean,
				LastSeen: now,
			})
		}
		return writeJSONAtomic(cachePath, cf)
	})
}

// @spec STORAGE-CACHE-002
func (s *Store) ListRepos() ([]CachedRepo, error) {
	lockPath := filepath.Join(s.CacheDir, "repos.lock")
	var raw []CachedRepo
	err := s.withLock(lockPath, func() error {
		cf, err := s.readCacheUnlocked()
		if err != nil {
			return err
		}
		raw = append(raw, cf.Repos...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	for i := range raw {
		raw[i].Name = filepath.Base(raw[i].Path)
		raw[i].Exists = IsRepoHealthy(raw[i].Path)
		if raw[i].Exists {
			if rems, err := ListRemotes(raw[i].Path); err == nil {
				raw[i].Remotes = rems
			}
		}
	}
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].LastSeen != raw[j].LastSeen {
			return raw[i].LastSeen > raw[j].LastSeen
		}
		return raw[i].Path < raw[j].Path
	})
	return raw, nil
}

// @spec STORAGE-CACHE-003
func (s *Store) RemoveRepo(path string) error {
	clean := filepath.Clean(path)
	lockPath := filepath.Join(s.CacheDir, "repos.lock")
	cachePath := filepath.Join(s.CacheDir, "repos.json")

	return s.withLock(lockPath, func() error {
		cf, err := s.readCacheUnlocked()
		if err != nil {
			return err
		}
		kept := make([]CachedRepo, 0, len(cf.Repos))
		for _, r := range cf.Repos {
			if filepath.Clean(r.Path) != clean {
				kept = append(kept, r)
			}
		}
		cf.Repos = kept
		return writeJSONAtomic(cachePath, cf)
	})
}

// @spec STORAGE-CACHE-004
func (s *Store) PruneMissingRepos() error {
	repos, err := s.ListRepos()
	if err != nil {
		return err
	}
	missing := make(map[string]bool)
	for _, r := range repos {
		if !r.Exists {
			missing[filepath.Clean(r.Path)] = true
		}
	}

	lockPath := filepath.Join(s.CacheDir, "repos.lock")
	cachePath := filepath.Join(s.CacheDir, "repos.json")
	return s.withLock(lockPath, func() error {
		cf, err := s.readCacheUnlocked()
		if err != nil {
			return err
		}
		kept := make([]CachedRepo, 0, len(cf.Repos))
		for _, r := range cf.Repos {
			if !missing[filepath.Clean(r.Path)] {
				kept = append(kept, r)
			}
		}
		cf.Repos = kept
		return writeJSONAtomic(cachePath, cf)
	})
}

func defaultConfig() Config {
	overrides := make(map[string]string, len(SupportedEditorIDs))
	for _, id := range SupportedEditorIDs {
		overrides[id] = ""
	}
	return Config{
		Port:            DefaultPort,
		EditorOverrides: overrides,
		Logging: LoggingConfig{
			Level:      "info",
			MaxSizeMB:  5,
			MaxBackups: 3,
		},
	}
}

func isValidLogLevel(lvl string) bool {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// @spec STORAGE-CFG-001, STORAGE-CFG-004, STORAGE-LOCK-002
func (s *Store) LoadConfig() (Config, error) {
	lockPath := filepath.Join(s.ConfigDir, "config.lock")
	cfgPath := filepath.Join(s.ConfigDir, "config.json")
	cfg := defaultConfig()

	err := s.withLock(lockPath, func() error {
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		var loaded Config
		if err := json.Unmarshal(data, &loaded); err != nil {
			_ = os.Rename(cfgPath, cfgPath+".corrupt")
			return nil
		}
		if loaded.Port >= 1 && loaded.Port <= 65535 {
			cfg.Port = loaded.Port
		}
		for _, id := range SupportedEditorIDs {
			if val, ok := loaded.EditorOverrides[id]; ok {
				cfg.EditorOverrides[id] = val
			}
		}
		if isValidLogLevel(loaded.Logging.Level) {
			cfg.Logging.Level = strings.ToLower(strings.TrimSpace(loaded.Logging.Level))
		}
		if loaded.Logging.MaxSizeMB >= 1 && loaded.Logging.MaxSizeMB <= 500 {
			cfg.Logging.MaxSizeMB = loaded.Logging.MaxSizeMB
		}
		if loaded.Logging.MaxBackups >= 0 && loaded.Logging.MaxBackups <= 20 {
			cfg.Logging.MaxBackups = loaded.Logging.MaxBackups
		}
		return nil
	})
	return cfg, err
}

func (s *Store) expandHome(p string) string {
	if strings.HasPrefix(p, "~/") && s.HomeDir != "" {
		return filepath.Join(s.HomeDir, strings.TrimPrefix(p, "~/"))
	}
	return p
}

// @spec STORAGE-CFG-004
func (s *Store) SaveLoggingConfig(lc LoggingConfig) error {
	lvl := strings.ToLower(strings.TrimSpace(lc.Level))
	if !isValidLogLevel(lvl) {
		return fmt.Errorf("invalid log level %q (must be debug, info, warn, or error)", lc.Level)
	}
	if lc.MaxSizeMB < 1 || lc.MaxSizeMB > 500 {
		return fmt.Errorf("max_size_mb must be between 1 and 500")
	}
	if lc.MaxBackups < 0 || lc.MaxBackups > 20 {
		return fmt.Errorf("max_backups must be between 0 and 20")
	}

	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Logging = LoggingConfig{
		Level:      lvl,
		MaxSizeMB:  lc.MaxSizeMB,
		MaxBackups: lc.MaxBackups,
	}

	lockPath := filepath.Join(s.ConfigDir, "config.lock")
	cfgPath := filepath.Join(s.ConfigDir, "config.json")
	return s.withLock(lockPath, func() error {
		return writeJSONAtomic(cfgPath, cfg)
	})
}

// @spec STORAGE-CFG-002
func (s *Store) SaveEditorOverrides(overrides map[string]string) error {
	allowed := make(map[string]bool, len(SupportedEditorIDs))
	for _, id := range SupportedEditorIDs {
		allowed[id] = true
	}

	cleaned := make(map[string]string, len(overrides))
	goos := s.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}

	for id, rawPath := range overrides {
		if !allowed[id] {
			return fmt.Errorf("unknown editor id %q", id)
		}
		trimmed := strings.TrimSpace(rawPath)
		if trimmed == "" {
			cleaned[id] = ""
			continue
		}
		expanded := s.expandHome(trimmed)
		info, err := os.Stat(expanded)
		if err != nil {
			return fmt.Errorf("override path for %s does not exist: %w", id, err)
		}
		if info.IsDir() {
			if goos != "darwin" || !strings.HasSuffix(expanded, ".app") {
				return fmt.Errorf("override path for %s is a directory", id)
			}
		}
		cleaned[id] = expanded
	}

	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	for id, val := range cleaned {
		cfg.EditorOverrides[id] = val
	}

	lockPath := filepath.Join(s.ConfigDir, "config.lock")
	cfgPath := filepath.Join(s.ConfigDir, "config.json")
	return s.withLock(lockPath, func() error {
		return writeJSONAtomic(cfgPath, cfg)
	})
}
