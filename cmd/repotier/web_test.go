package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// @spec WEB-SEC-001, WEB-SEC-002, WEB-API-001
func TestWebSecurityLoopbackOriginGuardAndErrorEnvelope(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	srv := NewServer(store, 45910, "v1.0.0")
	h := srv.Handler()

	// Cross-origin POST rejected with 403 and standard JSON error envelope
	req := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin POST, got %d", rec.Code)
	}
	var errBody map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody["error"] == "" {
		t.Fatalf("expected standard JSON error envelope, got %s", rec.Body.String())
	}

	// Localhost Origin allowed
	reqOK := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	reqOK.Header.Set("Origin", "http://127.0.0.1:45910")
	recOK := httptest.NewRecorder()
	h.ServeHTTP(recOK, reqOK)
	if recOK.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for valid localhost origin POST /api/shutdown, got %d", recOK.Code)
	}
}

// @spec WEB-API-002, WEB-API-003
func TestWebHealthAndShutdownEndpoints(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	srv := NewServer(store, 45910, "v1.2.3")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/health = %d; want 200", rec.Code)
	}
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		PID     int    `json:"pid"`
		Port    int    `json:"port"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || health.Version != "v1.2.3" || health.Port != 45910 || health.PID <= 0 {
		t.Fatalf("unexpected health response: %+v", health)
	}

	reqShut := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	recShut := httptest.NewRecorder()
	h.ServeHTTP(recShut, reqShut)
	if recShut.Code != http.StatusAccepted {
		t.Fatalf("POST /api/shutdown = %d; want 202", recShut.Code)
	}
	select {
	case <-srv.ShutdownCh:
	default:
		t.Fatalf("expected ShutdownCh to receive signal")
	}
}

// @spec WEB-API-004, WEB-API-005, WEB-API-006, WEB-API-007, WEB-API-013
func TestWebReposDetailsAndUpstreamEndpoints(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	repo := initTestGitRepo(t)
	remoteDir, _ := filepath.EvalSymlinks(t.TempDir())
	bareRemote := filepath.Join(remoteDir, "origin.git")
	mustGit(t, repo, "clone", "--bare", repo, bareRemote)
	mustGit(t, repo, "remote", "add", "origin", bareRemote)
	mustGit(t, repo, "fetch", "origin")

	_ = store.UpsertRepo(repo)
	_ = store.UpsertRepo(filepath.Join(t.TempDir(), "missing"))

	srv := NewServer(store, 45910, "v1.0.0")
	h := srv.Handler()

	// GET /api/repos
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/repos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/repos = %d", rec.Code)
	}

	// DELETE /api/repos prune_missing
	recPrune := httptest.NewRecorder()
	h.ServeHTTP(recPrune, httptest.NewRequest(http.MethodDelete, "/api/repos", strings.NewReader(`{"prune_missing":true}`)))
	if recPrune.Code != http.StatusOK {
		t.Fatalf("DELETE /api/repos prune_missing = %d", recPrune.Code)
	}

	// GET /api/repo/details
	recDet := httptest.NewRecorder()
	h.ServeHTTP(recDet, httptest.NewRequest(http.MethodGet, "/api/repo/details?path="+url.QueryEscape(repo), nil))
	if recDet.Code != http.StatusOK {
		t.Fatalf("GET /api/repo/details = %d", recDet.Code)
	}
	var det struct {
		Exists      bool       `json:"exists"`
		IsLocalOnly bool       `json:"is_local_only"`
		Remotes     []Remote   `json:"remotes"`
		Worktrees   []Worktree `json:"worktrees"`
		Branches    []Branch   `json:"branches"`
		RemoteRefs  []string   `json:"remote_refs"`
	}
	if err := json.Unmarshal(recDet.Body.Bytes(), &det); err != nil || !det.Exists || det.IsLocalOnly || len(det.Remotes) != 1 || len(det.Worktrees) != 1 || len(det.RemoteRefs) != 1 {
		t.Fatalf("unexpected repo details: %+v (err=%v)", det, err)
	}

	// GET /api/repo/remote-items on local-only repo
	localOnlyRepo := initTestGitRepo(t)
	recRemItems := httptest.NewRecorder()
	h.ServeHTTP(recRemItems, httptest.NewRequest(http.MethodGet, "/api/repo/remote-items?path="+url.QueryEscape(localOnlyRepo), nil))
	if recRemItems.Code != http.StatusOK {
		t.Fatalf("GET /api/repo/remote-items = %d", recRemItems.Code)
	}
	var remPayload struct {
		IsLocalOnly bool        `json:"is_local_only"`
		Issues      []IssueItem `json:"issues"`
		PRs         []PRItem    `json:"prs"`
	}
	if err := json.Unmarshal(recRemItems.Body.Bytes(), &remPayload); err != nil || !remPayload.IsLocalOnly {
		t.Fatalf("expected is_local_only=true for local repo, got %+v (err=%v)", remPayload, err)
	}

	// POST /api/branches/upstream
	upBody, _ := json.Marshal(map[string]string{
		"repo_path": repo,
		"branch":    "main",
		"upstream":  "origin/main",
	})
	recUp := httptest.NewRecorder()
	h.ServeHTTP(recUp, httptest.NewRequest(http.MethodPost, "/api/branches/upstream", bytes.NewReader(upBody)))
	if recUp.Code != http.StatusOK {
		t.Fatalf("POST /api/branches/upstream = %d (%s)", recUp.Code, recUp.Body.String())
	}
}

// @spec WEB-API-008, WEB-API-009
func TestWebBranchAndWorktreeDeletionEndpoints(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	repo := initTestGitRepo(t)
	wtParent, _ := filepath.EvalSymlinks(t.TempDir())
	wtDir := filepath.Join(wtParent, "wt-unmerged")
	mustGit(t, repo, "worktree", "add", "-b", "feat/wt-unmerged", wtDir)
	if err := os.WriteFile(filepath.Join(wtDir, "x.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wtDir, "add", "x.txt")
	mustGit(t, wtDir, "commit", "-m", "unmerged")

	srv := NewServer(store, 45910, "v1.0.0")
	h := srv.Handler()

	// DELETE /api/branches on checked-out branch -> 400 Bad Request
	bChecked, _ := json.Marshal(map[string]any{"repo_path": repo, "branch": "main", "force": true})
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodDelete, "/api/branches", bytes.NewReader(bChecked)))
	if rec1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 deleting checked-out branch, got %d", rec1.Code)
	}

	// DELETE /api/worktrees with delete_branch=true, force=false -> 409 Conflict (worktree_removed=true, requires_force=true)
	wtBody, _ := json.Marshal(map[string]any{
		"repo_path":      repo,
		"worktree_path":  wtDir,
		"branch":         "feat/wt-unmerged",
		"delete_branch":  true,
		"force":          false,
		"force_worktree": false,
	})
	recWT := httptest.NewRecorder()
	h.ServeHTTP(recWT, httptest.NewRequest(http.MethodDelete, "/api/worktrees", bytes.NewReader(wtBody)))
	if recWT.Code != http.StatusConflict {
		t.Fatalf("expected 409 on unmerged branch after worktree removal, got %d: %s", recWT.Code, recWT.Body.String())
	}

	// Retry branch delete with force=true via DELETE /api/branches -> 200 OK
	bRetry, _ := json.Marshal(map[string]any{"repo_path": repo, "branch": "feat/wt-unmerged", "force": true})
	recRetry := httptest.NewRecorder()
	h.ServeHTTP(recRetry, httptest.NewRequest(http.MethodDelete, "/api/branches", bytes.NewReader(bRetry)))
	if recRetry.Code != http.StatusOK {
		t.Fatalf("expected 200 on force branch delete retry, got %d: %s", recRetry.Code, recRetry.Body.String())
	}
}

// @spec WEB-API-010, WEB-API-011, WEB-API-012, WEB-EDIT-001, WEB-EDIT-002
func TestWebEditorsResolutionSettingsAndLaunch(t *testing.T) {
	homeDir := t.TempDir()
	store := NewStore(t.TempDir(), t.TempDir())
	store.HomeDir = homeDir
	store.GOOS = "darwin"

	fakeBin := filepath.Join(homeDir, "bin", "code")
	_ = os.MkdirAll(filepath.Dir(fakeBin), 0755)
	_ = os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0755)

	fakeAppDir := filepath.Join(homeDir, "Applications")
	fakeAgyApp := filepath.Join(fakeAppDir, "Antigravity.app")
	_ = os.MkdirAll(fakeAgyApp, 0755)

	var launchedCmd string
	var launchedArgs []string

	srv := NewServer(store, 45910, "v1.0.0")
	srv.EditorEnv = &EditorResolver{
		GOOS:    "darwin",
		HomeDir: homeDir,
		AppDirs: []string{fakeAppDir},
		LookPath: func(name string) (string, error) {
			if name == "code" {
				return fakeBin, nil
			}
			return "", errors.New("not found")
		},
		ExecStart: func(name string, args ...string) error {
			launchedCmd = name
			launchedArgs = args
			return nil
		},
	}
	h := srv.Handler()

	// GET /api/editors
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/editors", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/editors = %d", rec.Code)
	}

	// PUT /api/settings/editors with override pointing to .app bundle
	putBody, _ := json.Marshal(map[string]any{
		"overrides": map[string]string{"agy": fakeAgyApp},
	})
	recPut := httptest.NewRecorder()
	h.ServeHTTP(recPut, httptest.NewRequest(http.MethodPut, "/api/settings/editors", bytes.NewReader(putBody)))
	if recPut.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings/editors = %d: %s", recPut.Code, recPut.Body.String())
	}

	// POST /api/worktrees/open with empty worktree_path (falls back to HomeDir) and darwin .app -> open -a
	openBody, _ := json.Marshal(map[string]string{
		"editor_id":     "agy",
		"worktree_path": "",
	})
	recOpen := httptest.NewRecorder()
	h.ServeHTTP(recOpen, httptest.NewRequest(http.MethodPost, "/api/worktrees/open", bytes.NewReader(openBody)))
	if recOpen.Code != http.StatusOK {
		t.Fatalf("POST /api/worktrees/open = %d: %s", recOpen.Code, recOpen.Body.String())
	}
	if launchedCmd != "open" || len(launchedArgs) < 3 || launchedArgs[0] != "-a" || launchedArgs[1] != fakeAgyApp || launchedArgs[2] != homeDir {
		t.Fatalf("expected open -a %q %q, got %q %v", fakeAgyApp, homeDir, launchedCmd, launchedArgs)
	}
}

// @spec WEB-UI-001, WEB-UI-002, WEB-UI-003, WEB-UI-004, WEB-UI-005, WEB-UI-006, WEB-UI-007, WEB-UI-008, WEB-UI-009
func TestWebEmbeddedUIDashboardContract(t *testing.T) {
	store := NewStore(t.TempDir(), t.TempDir())
	srv := NewServer(store, 45910, "v1.0.0")
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	requiredTokens := []string{
		"Repository folder is no longer present or reachable",
		"visibilitychange",
		"Also delete local branch",
		"Force delete unmerged branch (-D)",
		"Force remove worktree (--force)",
		"Checked out in main repository directory",
		"Prune Unreachable",
		"Test Launch",
		"Shutdown Daemon",
		"/api/worktrees/open",
		"/api/branches/upstream",
		"history.replaceState",
		"Repositories",
		"This repository is local-only (no Git remotes configured)",
		"local-no-remote",
		"/api/repo/remote-items",
		"Pull Requests",
		"Issues",
	}
	for _, tok := range requiredTokens {
		if !strings.Contains(body, tok) {
			t.Fatalf("embedded ui/index.html missing required UI token %q", tok)
		}
	}
}
