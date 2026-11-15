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
		Store:      store,
		CWD:        repo,
		Stdout:     &stdout,
		Stderr:     &stderr,
		Version:    "v1.0.0",
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

// @spec CLI-HOOK-001, CLI-HOOK-002, CLI-HOOK-003, CLI-HOOK-004, CLI-HOOK-005
func TestCLIPreRunHookDiscoveryPortAndAutoStart(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
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

	var apiSrv *httptest.Server
	apiSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset.tar.gz" {
			_, _ = w.Write(tarBuf.Bytes())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v2.0.0",
			"assets": []map[string]any{
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
		Version:       "v1.0.0",
	}
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
