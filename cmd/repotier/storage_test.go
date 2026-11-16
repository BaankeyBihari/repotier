package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// @spec STORAGE-CACHE-001, STORAGE-CACHE-002, STORAGE-CACHE-003, STORAGE-CACHE-004
func TestStorageRepoCacheCRUDAndPrune(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	validRepo := initTestGitRepo(t)
	missingRepo := filepath.Join(t.TempDir(), "deleted-repo")

	if err := store.UpsertRepo(missingRepo); err != nil {
		t.Fatalf("UpsertRepo(missing) failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := store.UpsertRepo(validRepo); err != nil {
		t.Fatalf("UpsertRepo(valid) failed: %v", err)
	}

	repos, err := store.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos failed: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 cached repos, got %d", len(repos))
	}
	// Sorted by last_seen descending -> validRepo first
	if repos[0].Path != validRepo || !repos[0].Exists || repos[0].Name != filepath.Base(validRepo) {
		t.Fatalf("unexpected first repo: %+v", repos[0])
	}
	if repos[1].Path != missingRepo || repos[1].Exists {
		t.Fatalf("unexpected second repo: %+v", repos[1])
	}

	if err := store.PruneMissingRepos(); err != nil {
		t.Fatalf("PruneMissingRepos failed: %v", err)
	}
	afterPrune, err := store.ListRepos()
	if err != nil || len(afterPrune) != 1 || afterPrune[0].Path != validRepo {
		t.Fatalf("expected only validRepo after prune, got %+v (err=%v)", afterPrune, err)
	}

	if err := store.RemoveRepo(validRepo); err != nil {
		t.Fatalf("RemoveRepo failed: %v", err)
	}
	afterRemove, err := store.ListRepos()
	if err != nil || len(afterRemove) != 0 {
		t.Fatalf("expected 0 repos after remove, got %+v (err=%v)", afterRemove, err)
	}
}

// @spec STORAGE-CFG-001, STORAGE-CFG-002
func TestStorageConfigDefaultsAndEditorOverrides(t *testing.T) {
	homeDir := t.TempDir()
	store := NewStore(t.TempDir(), t.TempDir())
	store.HomeDir = homeDir
	store.GOOS = "darwin"

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Port != DefaultPort || len(cfg.EditorOverrides) != 5 {
		t.Fatalf("unexpected default config: %+v", cfg)
	}

	fakeBin := filepath.Join(homeDir, "bin", "custom-code")
	if err := os.MkdirAll(filepath.Dir(fakeBin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	fakeApp := filepath.Join(homeDir, "Applications", "Antigravity.app")
	if err := os.MkdirAll(fakeApp, 0755); err != nil {
		t.Fatal(err)
	}

	// Reject unknown editor ID
	if err := store.SaveEditorOverrides(map[string]string{"unknown": fakeBin}); err == nil {
		t.Fatalf("expected error for unknown editor ID")
	}

	// Reject non-existent path
	if err := store.SaveEditorOverrides(map[string]string{"vscode": "/no/such/binary"}); err == nil {
		t.Fatalf("expected error for non-existent override path")
	}

	// Accept ~/ expansion and darwin .app directory
	if err := store.SaveEditorOverrides(map[string]string{
		"vscode": "~/bin/custom-code",
		"agy":    fakeApp,
	}); err != nil {
		t.Fatalf("SaveEditorOverrides failed: %v", err)
	}

	loaded, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EditorOverrides["vscode"] != fakeBin || loaded.EditorOverrides["agy"] != fakeApp {
		t.Fatalf("unexpected saved overrides: %+v", loaded.EditorOverrides)
	}
}

// @spec STORAGE-LOCK-001, STORAGE-LOCK-002
func TestStorageConcurrencyLockAndCorruptRecovery(t *testing.T) {
	cacheDir := t.TempDir()
	configDir := t.TempDir()
	store := NewStore(cacheDir, configDir)

	// Write corrupt JSON to both files
	if err := os.WriteFile(filepath.Join(cacheDir, "repos.json"), []byte("{corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{corrupt"), 0644); err != nil {
		t.Fatal(err)
	}

	repos, err := store.ListRepos()
	if err != nil || len(repos) != 0 {
		t.Fatalf("expected empty repos after corrupt recovery, got %v, %v", repos, err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "repos.json.corrupt")); err != nil {
		t.Fatalf("expected repos.json.corrupt backup to exist: %v", err)
	}

	cfg, err := store.LoadConfig()
	if err != nil || cfg.Port != DefaultPort {
		t.Fatalf("expected default config after corrupt recovery, got %+v, %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.json.corrupt")); err != nil {
		t.Fatalf("expected config.json.corrupt backup to exist: %v", err)
	}

	// Concurrent upserts
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := filepath.Join(cacheDir, "repo", string(rune('a'+idx)))
			_ = store.UpsertRepo(p)
		}(i)
	}
	wg.Wait()

	finalRepos, err := store.ListRepos()
	if err != nil || len(finalRepos) != 8 {
		t.Fatalf("expected 8 repos after concurrent upserts, got %d (err=%v)", len(finalRepos), err)
	}
}

// @spec STORAGE-CFG-004
func TestStorageLoggingConfigDefaultsAndValidation(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.MaxSizeMB != 5 || cfg.Logging.MaxBackups != 3 {
		t.Fatalf("unexpected default logging config: %+v", cfg.Logging)
	}

	if err := store.SaveLoggingConfig(LoggingConfig{Level: "bogus", MaxSizeMB: 5, MaxBackups: 3}); err == nil {
		t.Fatalf("expected error for invalid log level")
	}
	if err := store.SaveLoggingConfig(LoggingConfig{Level: "debug", MaxSizeMB: 0, MaxBackups: 3}); err == nil {
		t.Fatalf("expected error for max_size_mb=0")
	}
	if err := store.SaveLoggingConfig(LoggingConfig{Level: "debug", MaxSizeMB: 10, MaxBackups: 4}); err != nil {
		t.Fatalf("SaveLoggingConfig failed: %v", err)
	}
	reloaded, err := store.LoadConfig()
	if err != nil || reloaded.Logging.Level != "debug" || reloaded.Logging.MaxSizeMB != 10 || reloaded.Logging.MaxBackups != 4 {
		t.Fatalf("unexpected reloaded logging config: %+v (err=%v)", reloaded.Logging, err)
	}
}
