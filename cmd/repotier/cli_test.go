package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// @spec CLI-CMD-001, CLI-CMD-002
func TestCLISubcommandDispatchAndValidation(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	repo := initTestGitRepo(t)
	var stdout, stderr bytes.Buffer
	var openedURL string

	// Start an in-process test server on an ephemeral port and write daemon.port
	srv := NewServer(store, 0, "v1.0.0")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	srv.Port = port
	go http.Serve(ln, srv.Handler())
	_ = os.WriteFile(filepath.Join(store.CacheDir, "daemon.port"), []byte(fmt.Sprintf("%d", port)), 0644)

	env := &CLIEnv{
		Store:   store,
		CWD:     repo,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Version: "v1.0.0",
		BrowserOpen: func(u string) error {
			openedURL = u
			return nil
		},
	}

	// Unknown subcommand -> exit 2
	if code := env.Run([]string{"bogus-cmd"}); code != 2 {
		t.Fatalf("expected exit code 2 for unknown subcommand, got %d", code)
	}

	// Out-of-range daemon --port -> exit 2
	if code := env.Run([]string{"daemon", "--port", "70000"}); code != 2 {
		t.Fatalf("expected exit code 2 for invalid port, got %d", code)
	}

	// Default dashboard subcommand inside repo -> opens URL with ?repo=
	if code := env.Run(nil); code != 0 {
		t.Fatalf("expected exit code 0 for default dashboard run, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.HasPrefix(openedURL, fmt.Sprintf("http://127.0.0.1:%d?repo=", port)) {
		t.Fatalf("expected browser URL with ?repo=, got %q", openedURL)
	}
}

func writeIsolatedPortConfig(t *testing.T, store *Store) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return DefaultPort
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	_ = os.WriteFile(filepath.Join(store.ConfigDir, "config.json"), []byte(fmt.Sprintf(`{"port":%d}`, port)), 0644)
	return port
}

// @spec CLI-HOOK-001, CLI-HOOK-002, CLI-HOOK-003, CLI-HOOK-004, CLI-HOOK-005
func TestCLIPreRunHookDiscoveryPortAndAutoStart(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	_ = writeIsolatedPortConfig(t, store)
	nonGitDir := t.TempDir()

	// Write a stale daemon.port (port 1 is unreachable)
	_ = os.WriteFile(filepath.Join(store.CacheDir, "daemon.port"), []byte("1"), 0644)

	// Start a background listener when SpawnDaemon is triggered
	var ln net.Listener
	defer func() {
		if ln != nil {
			ln.Close()
		}
	}()

	env := &CLIEnv{
		Store:   store,
		CWD:     nonGitDir,
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Version: "v1.0.0",
		SpawnDaemon: func(port int) error {
			var err error
			ln, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return err
			}
			actualPort := ln.Addr().(*net.TCPAddr).Port
			_ = os.WriteFile(filepath.Join(store.CacheDir, "daemon.port"), []byte(fmt.Sprintf("%d", actualPort)), 0644)
			srv := NewServer(store, actualPort, "v1.0.0")
			go http.Serve(ln, srv.Handler())
			return nil
		},
	}

	// Outside Git repo: skips cache insertion without error and auto-starts daemon
	canonical, activePort, err := env.RunPreRunHook()
	if err != nil || canonical != "" || activePort <= 0 {
		t.Fatalf("RunPreRunHook outside git repo = %q, %d, %v", canonical, activePort, err)
	}

	// Startup timeout error when daemon fails to become healthy
	storeFail := NewStore(t.TempDir(), t.TempDir())
	_ = writeIsolatedPortConfig(t, storeFail)
	envFail := &CLIEnv{
		Store:   storeFail,
		CWD:     nonGitDir,
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Version: "v1.0.0",
		SpawnDaemon: func(port int) error {
			return nil // never starts listener
		},
	}
	if _, _, err := envFail.RunPreRunHook(); err == nil {
		t.Fatalf("expected timeout error when spawned daemon never becomes healthy")
	}
}

// @spec CLI-DAEMON-001, CLI-DAEMON-002, CLI-DAEMON-003, CLI-DAEMON-004
func TestCLIDaemonLifecycleAndShutdown(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	_ = writeIsolatedPortConfig(t, store)
	var stdout, stderr bytes.Buffer
	env := &CLIEnv{
		Store:   store,
		CWD:     t.TempDir(),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Version: "v1.0.0",
	}

	stopCh := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.RunDaemon(0, stopCh)
	}()

	// Wait for daemon.port and daemon.pid to appear
	var port int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(store.CacheDir, "daemon.port"))
		if err == nil {
			_, _ = fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &port)
			if port > 0 {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if port == 0 {
		t.Fatalf("daemon did not write daemon.port in time")
	}
	if _, err := os.Stat(filepath.Join(store.CacheDir, "daemon.pid")); err != nil {
		t.Fatalf("expected daemon.pid to exist: %v", err)
	}

	// RunShutdown should stop the daemon and remove PID/port files
	if err := env.RunShutdown(); err != nil {
		t.Fatalf("RunShutdown failed: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunDaemon returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.CacheDir, "daemon.pid")); !os.IsNotExist(err) {
		t.Fatalf("expected daemon.pid to be removed on exit")
	}

	// Second RunShutdown when unreachable -> cleans stale files and prints "Daemon is not running"
	_ = os.WriteFile(filepath.Join(store.CacheDir, "daemon.pid"), []byte("99999"), 0644)
	stdout.Reset()
	if err := env.RunShutdown(); err != nil {
		t.Fatalf("RunShutdown when unreachable failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "Daemon is not running") {
		t.Fatalf("expected 'Daemon is not running' output, got %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(store.CacheDir, "daemon.pid")); !os.IsNotExist(err) {
		t.Fatalf("expected stale daemon.pid to be cleaned up")
	}
}

// @spec CLI-UPDATE-001, CLI-UPDATE-002
func TestCLISelfUpdateAndDaemonRestart(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	binDir := t.TempDir()
	fakeExec := filepath.Join(binDir, "repotier")
	if err := os.WriteFile(fakeExec, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	// Build a tar.gz payload containing the updated "repotier" binary
	var tarBuf bytes.Buffer
	gz := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gz)
	newContent := []byte("new-binary-v2.0.0")
	_ = tw.WriteHeader(&tar.Header{
		Name: "repotier",
		Mode: 0755,
		Size: int64(len(newContent)),
	})
	_, _ = tw.Write(newContent)
	_ = tw.Close()
	_ = gz.Close()

	var remoteTag = "v2026.11.2"
	var return404 = true
	var gotAcceptHeader string
	var apiSrv *httptest.Server
	apiSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset.tar.gz" {
			_, _ = w.Write(tarBuf.Bytes())
			return
		}
		gotAcceptHeader = r.Header.Get("Accept")
		if return404 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": remoteTag,
			"assets": []map[string]any{
				{
					"name":                 "checksums.txt",
					"browser_download_url": apiSrv.URL + "/checksums.txt",
				},
				{
					"name":                 "repotier_darwin_arm64.tar.gz",
					"browser_download_url": apiSrv.URL + "/asset.tar.gz",
				},
			},
		})
	}))
	defer apiSrv.Close()

	// Start a running daemon so update exercises shutdown + restart
	stopCh := make(chan struct{})
	env := &CLIEnv{
		Store:         store,
		Executable:    fakeExec,
		CWD:           t.TempDir(),
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
		ReleaseAPIURL: apiSrv.URL,
		GOOS:          "darwin",
		GOARCH:        "arm64",
		Version:       "2026.11.10",
	}

	// 0. 404 Not Found (no releases published yet) -> graceful no-op
	if err := env.RunUpdate(); err != nil {
		t.Fatalf("expected 404 from releases/latest to succeed as no-op, got: %v", err)
	}
	if gotAcceptHeader != "application/vnd.github+json" {
		t.Fatalf("expected Accept: application/vnd.github+json header, got %q", gotAcceptHeader)
	}
	return404 = false

	// 1. CalVer tuple check: current 2026.11.10 > remote v2026.11.2 -> must NOT downgrade
	if err := env.RunUpdate(); err != nil {
		t.Fatalf("RunUpdate(no-downgrade) failed: %v", err)
	}
	afterNoOp, err := os.ReadFile(fakeExec)
	if err != nil || string(afterNoOp) != "old-binary" {
		t.Fatalf("expected binary NOT to downgrade when current 2026.11.10 > remote v2026.11.2, got %q", afterNoOp)
	}

	// 2. CalVer tuple check: current 2026.11.2 < remote v2026.11.10 -> must update & restart daemon
	env.Version = "2026.11.2"
	remoteTag = "v2026.11.10"
	go func() { _ = env.RunDaemon(0, stopCh) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(store.CacheDir, "daemon.port")); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	restarted := false
	env.SpawnDaemon = func(port int) error {
		restarted = true
		return nil
	}

	if err := env.RunUpdate(); err != nil {
		t.Fatalf("RunUpdate failed: %v", err)
	}
	updatedBytes, err := os.ReadFile(fakeExec)
	if err != nil || string(updatedBytes) != string(newContent) {
		t.Fatalf("expected binary to be replaced with %q, got %q (err=%v)", newContent, updatedBytes, err)
	}
	if !restarted {
		t.Fatalf("expected running daemon to be restarted after self-update")
	}
}

// @spec CLI-BUILD-001
func TestCLIBuildAndPreCommitToolingContract(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cwd) != "repotier" || filepath.Base(filepath.Dir(cwd)) != "cmd" {
		t.Fatalf("expected package to reside in cmd/repotier, got %s", cwd)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	miseBytes, err := os.ReadFile(filepath.Join(repoRoot, "mise.toml"))
	if err != nil {
		t.Fatalf("missing mise.toml: %v", err)
	}
	miseText := string(miseBytes)
	for _, want := range []string{`go = "1.27"`, "golangci-lint", "pre-commit", "[tasks.build]", "[tasks.clean]", "[tasks.lint]", "./cmd/repotier"} {
		if !strings.Contains(miseText, want) {
			t.Fatalf("mise.toml missing %q", want)
		}
	}

	modBytes, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil || !strings.Contains(string(modBytes), "go 1.27") {
		t.Fatalf("expected go.mod to specify go 1.27, got: %s (err=%v)", string(modBytes), err)
	}

	pcBytes, err := os.ReadFile(filepath.Join(repoRoot, ".pre-commit-config.yaml"))
	if err != nil || !strings.Contains(string(pcBytes), "golangci-lint") {
		t.Fatalf("missing or incomplete .pre-commit-config.yaml: %v", err)
	}

	if _, err := os.Stat(filepath.Join(repoRoot, ".golangci.yml")); err != nil {
		t.Fatalf("missing .golangci.yml: %v", err)
	}

	wfBytes, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("missing .github/workflows/release.yml: %v", err)
	}
	wfText := string(wfBytes)
	for _, want := range []string{"gh release create", "checksums.txt", "repotier_"} {
		if !strings.Contains(wfText, want) {
			t.Fatalf(".github/workflows/release.yml missing %q", want)
		}
	}
}
