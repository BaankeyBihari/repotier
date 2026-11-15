package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type CLIEnv struct {
	Store         *Store
	Executable    string
	CWD           string
	Stdout        io.Writer
	Stderr        io.Writer
	BrowserOpen   func(url string) error
	ReleaseAPIURL string
	GOOS          string
	GOARCH        string
	Version       string
	SpawnDaemon   func(port int) error
}

func DefaultCLIEnv(store *Store) *CLIEnv {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	return &CLIEnv{
		Store:         store,
		Executable:    exe,
		CWD:           cwd,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		ReleaseAPIURL: "https://api.github.com/repos/BaankeyBihari/repotier/releases/latest",
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		Version:       Version,
		BrowserOpen:   defaultBrowserOpen,
	}
}

func defaultBrowserOpen(targetURL string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", targetURL)
	} else {
		cmd = exec.Command("xdg-open", targetURL)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// @spec CLI-CMD-001, CLI-CMD-002
func (env *CLIEnv) Run(args []string) int {
	sub := "dashboard"
	var rest []string
	if len(args) > 0 {
		sub = args[0]
		rest = args[1:]
	}

	switch sub {
	case "dashboard":
		if len(rest) > 0 {
			env.printUsage()
			return 2
		}
		repoRoot, port, err := env.RunPreRunHook()
		if err != nil {
			fmt.Fprintf(env.Stderr, "repotier error: %v\n", err)
			return 1
		}
		targetURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		if repoRoot != "" {
			targetURL += "?repo=" + url.QueryEscape(repoRoot)
		}
		if env.BrowserOpen != nil {
			_ = env.BrowserOpen(targetURL)
		}
		return 0

	case "daemon":
		fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
		fs.SetOutput(env.Stderr)
		cfg, _ := env.Store.LoadConfig()
		portFlag := fs.Int("port", cfg.Port, "localhost port to bind")
		if err := fs.Parse(rest); err != nil || fs.NArg() > 0 || *portFlag < 0 || *portFlag > 65535 {
			env.printUsage()
			return 2
		}
		if err := env.RunDaemon(*portFlag, nil); err != nil {
			fmt.Fprintf(env.Stderr, "daemon error: %v\n", err)
			return 1
		}
		return 0

	case "shutdown":
		if len(rest) > 0 {
			env.printUsage()
			return 2
		}
		if err := env.RunShutdown(); err != nil {
			fmt.Fprintf(env.Stderr, "shutdown error: %v\n", err)
			return 1
		}
		return 0

	case "update":
		if len(rest) > 0 {
			env.printUsage()
			return 2
		}
		_, _, _ = env.RunPreRunHook()
		if err := env.RunUpdate(); err != nil {
			fmt.Fprintf(env.Stderr, "update error: %v\n", err)
			return 1
		}
		return 0

	default:
		env.printUsage()
		return 2
	}
}

func (env *CLIEnv) printUsage() {
	if env.Stderr != nil {
		fmt.Fprintln(env.Stderr, "Usage: repotier [dashboard | daemon [--port <port>] | shutdown | update]")
	}
}

func probeHealth(port int, timeout time.Duration) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (env *CLIEnv) readPortFile() int {
	data, err := os.ReadFile(filepath.Join(env.Store.CacheDir, "daemon.port"))
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || p < 1 || p > 65535 {
		return 0
	}
	return p
}

// @spec CLI-HOOK-003
func (env *CLIEnv) ResolveActivePort() int {
	if p := env.readPortFile(); p > 0 && probeHealth(p, 200*time.Millisecond) {
		return p
	}
	cfg, err := env.Store.LoadConfig()
	if err == nil && cfg.Port >= 1 && cfg.Port <= 65535 {
		return cfg.Port
	}
	return DefaultPort
}

// @spec CLI-HOOK-001, CLI-HOOK-002, CLI-HOOK-003, CLI-HOOK-004, CLI-HOOK-005
func (env *CLIEnv) RunPreRunHook() (string, int, error) {
	var canonicalRepo string
	if env.CWD != "" {
		if root, err := DiscoverRepoRoot(env.CWD); err == nil && root != "" {
			canonicalRepo = root
			_ = env.Store.UpsertRepo(root)
		}
	}

	if p := env.readPortFile(); p > 0 && probeHealth(p, 200*time.Millisecond) {
		return canonicalRepo, p, nil
	}

	cfg, _ := env.Store.LoadConfig()
	targetPort := cfg.Port
	if targetPort <= 0 || targetPort > 65535 {
		targetPort = DefaultPort
	}
	if probeHealth(targetPort, 200*time.Millisecond) {
		return canonicalRepo, targetPort, nil
	}

	if err := env.spawnDetachedDaemon(targetPort); err != nil {
		return canonicalRepo, 0, err
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		checkPort := env.readPortFile()
		if checkPort == 0 {
			checkPort = targetPort
		}
		if probeHealth(checkPort, 200*time.Millisecond) {
			return canonicalRepo, checkPort, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return canonicalRepo, 0, errors.New("daemon failed to become healthy within 3s")
}

func (env *CLIEnv) spawnDetachedDaemon(port int) error {
	if env.SpawnDaemon != nil {
		return env.SpawnDaemon(port)
	}
	exe := env.Executable
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return err
		}
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "daemon", "--port", strconv.Itoa(port))
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// @spec CLI-DAEMON-001, CLI-DAEMON-002
func (env *CLIEnv) RunDaemon(port int, stopCh <-chan struct{}) error {
	if err := os.MkdirAll(env.Store.CacheDir, 0755); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	pidFile := filepath.Join(env.Store.CacheDir, "daemon.pid")
	portFile := filepath.Join(env.Store.CacheDir, "daemon.port")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		ln.Close()
		return err
	}
	if err := os.WriteFile(portFile, []byte(strconv.Itoa(actualPort)), 0644); err != nil {
		_ = os.Remove(pidFile)
		ln.Close()
		return err
	}
	defer func() {
		_ = os.Remove(pidFile)
		_ = os.Remove(portFile)
	}()

	srv := NewServer(env.Store, actualPort, env.Version)
	httpSrv := &http.Server{Handler: srv.Handler()}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-sigCh:
	case <-srv.ShutdownCh:
	case <-stopCh:
	case err := <-errCh:
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	return <-errCh
}

// @spec CLI-DAEMON-003, CLI-DAEMON-004
func (env *CLIEnv) RunShutdown() error {
	port := env.readPortFile()
	if port == 0 {
		cfg, _ := env.Store.LoadConfig()
		port = cfg.Port
	}
	if port <= 0 {
		port = DefaultPort
	}

	pidFile := filepath.Join(env.Store.CacheDir, "daemon.pid")
	portFile := filepath.Join(env.Store.CacheDir, "daemon.port")

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/shutdown", port), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = os.Remove(pidFile)
		_ = os.Remove(portFile)
		if env.Stdout != nil {
			fmt.Fprintln(env.Stdout, "Daemon is not running")
		}
		return nil
	}
	resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !probeHealth(port, 100*time.Millisecond) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = os.Remove(pidFile)
	_ = os.Remove(portFile)
	if env.Stdout != nil {
		fmt.Fprintln(env.Stdout, "Daemon stopped")
	}
	return nil
}

// @spec CLI-UPDATE-001, CLI-UPDATE-002
func (env *CLIEnv) RunUpdate() error {
	activePort := env.readPortFile()
	wasRunning := activePort > 0 && probeHealth(activePort, 200*time.Millisecond)

	apiURL := env.ReleaseAPIURL
	if apiURL == "" {
		apiURL = "https://api.github.com/repos/BaankeyBihari/repotier/releases/latest"
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release check returned HTTP %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return err
	}

	if release.TagName == "" || release.TagName == env.Version {
		if env.Stdout != nil {
			fmt.Fprintf(env.Stdout, "repotier is already up to date (%s)\n", env.Version)
		}
		return nil
	}

	goos := env.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := env.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	var downloadURL, assetName string
	for _, a := range release.Assets {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, goos) && strings.Contains(lower, goarch) {
			downloadURL = a.BrowserDownloadURL
			assetName = a.Name
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no release asset found for %s/%s", goos, goarch)
	}

	dlResp, err := client.Get(downloadURL)
	if err != nil {
		return err
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("asset download returned HTTP %d", dlResp.StatusCode)
	}

	exePath := env.Executable
	if exePath == "" {
		exePath, err = os.Executable()
		if err != nil {
			return err
		}
	}
	tmpPath := exePath + ".update.tmp"
	if err := writeUpdatedBinary(tmpPath, assetName, dlResp.Body); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if wasRunning {
		_ = env.RunShutdown()
		cfg, _ := env.Store.LoadConfig()
		portToStart := activePort
		if portToStart <= 0 {
			portToStart = cfg.Port
		}
		if err := env.spawnDetachedDaemon(portToStart); err != nil {
			return err
		}
	}
	return nil
}

func writeUpdatedBinary(tmpPath, assetName string, r io.Reader) error {
	outFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer outFile.Close()

	if strings.HasSuffix(strings.ToLower(assetName), ".tar.gz") || strings.HasSuffix(strings.ToLower(assetName), ".tgz") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if hdr.Typeflag == tar.TypeReg && (filepath.Base(hdr.Name) == "repotier" || strings.HasSuffix(hdr.Name, "repotier")) {
				_, err := io.Copy(outFile, tr)
				return err
			}
		}
		return errors.New("repotier binary not found in tar.gz archive")
	}

	_, err = io.Copy(outFile, r)
	return err
}
