package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// @spec WEB-UI-001
//
//go:embed ui/index.html
var embeddedUI []byte

type editorSpec struct {
	ID         string
	Label      string
	Binaries   []string
	ExtraPaths []string
	MacApps    []string
}

var supportedEditors = []editorSpec{
	{
		ID:         "agy",
		Label:      "Antigravity IDE",
		Binaries:   []string{"agy", "agy-ide"},
		ExtraPaths: []string{"/Applications/Antigravity.app/Contents/Resources/app/bin/agy"},
		MacApps:    []string{"Antigravity"},
	},
	{
		ID:         "vscode",
		Label:      "VS Code",
		Binaries:   []string{"code"},
		ExtraPaths: []string{"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"},
		MacApps:    []string{"Visual Studio Code"},
	},
	{
		ID:         "windsurf",
		Label:      "Windsurf / Devin Desktop",
		Binaries:   []string{"windsurf", "surf", "devin"},
		ExtraPaths: []string{"/Applications/Windsurf.app/Contents/Resources/app/bin/windsurf"},
		MacApps:    []string{"Windsurf", "Devin"},
	},
	{
		ID:       "intellij",
		Label:    "IntelliJ IDEA",
		Binaries: []string{"idea"},
		ExtraPaths: []string{
			"~/Library/Application Support/JetBrains/Toolbox/scripts/idea",
			"/Applications/IntelliJ IDEA.app/Contents/MacOS/idea",
			"/Applications/IntelliJ IDEA CE.app/Contents/MacOS/idea",
		},
		MacApps: []string{"IntelliJ IDEA", "IntelliJ IDEA CE"},
	},
	{
		ID:         "subl",
		Label:      "Sublime Text",
		Binaries:   []string{"subl"},
		ExtraPaths: []string{"/Applications/Sublime Text.app/Contents/SharedSupport/bin/subl"},
		MacApps:    []string{"Sublime Text"},
	},
}

type EditorInfo struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	DetectedPath string `json:"detected_path"`
	OverridePath string `json:"override_path"`
	Available    bool   `json:"available"`
}

type EditorResolver struct {
	GOOS       string
	HomeDir    string
	AppDirs    []string
	ExtraPaths map[string][]string
	LookPath   func(string) (string, error)
	ExecStart  func(name string, args ...string) error
}

func defaultEditorResolver(store *Store) *EditorResolver {
	home := ""
	goos := runtime.GOOS
	if store != nil {
		home = store.HomeDir
		if store.GOOS != "" {
			goos = store.GOOS
		}
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return &EditorResolver{
		GOOS:     goos,
		HomeDir:  home,
		AppDirs:  []string{"/Applications"},
		LookPath: exec.LookPath,
		ExecStart: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait() }()
			return nil
		},
	}
}

type Server struct {
	Store      *Store
	Port       int
	Version    string
	ShutdownCh chan struct{}
	EditorEnv  *EditorResolver
}

func NewServer(store *Store, port int, version string) *Server {
	return &Server{
		Store:      store,
		Port:       port,
		Version:    version,
		ShutdownCh: make(chan struct{}, 1),
		EditorEnv:  defaultEditorResolver(store),
	}
}

// @spec WEB-API-001
func writeJSONError(w http.ResponseWriter, status int, msg, gitErr string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := map[string]string{"error": msg}
	if gitErr != "" {
		resp["git_error"] = gitErr
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// @spec WEB-SEC-001, WEB-SEC-002
func (s *Server) localhostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin != "" {
				expected1 := fmt.Sprintf("http://127.0.0.1:%d", s.Port)
				expected2 := fmt.Sprintf("http://localhost:%d", s.Port)
				if origin != expected1 && origin != expected2 {
					writeJSONError(w, http.StatusForbidden, "forbidden origin", "")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("GET /api/repos", s.handleGetRepos)
	mux.HandleFunc("DELETE /api/repos", s.handleDeleteRepos)
	mux.HandleFunc("GET /api/repo/details", s.handleRepoDetails)
	mux.HandleFunc("GET /api/repo/remote-items", s.handleRepoRemoteItems)
	mux.HandleFunc("POST /api/repo/fetch", s.handleRepoFetch)
	mux.HandleFunc("POST /api/branches/upstream", s.handleSetUpstream)
	mux.HandleFunc("DELETE /api/branches", s.handleDeleteBranch)
	mux.HandleFunc("DELETE /api/worktrees", s.handleDeleteWorktree)
	mux.HandleFunc("GET /api/editors", s.handleGetEditors)
	mux.HandleFunc("PUT /api/settings/editors", s.handlePutEditors)
	mux.HandleFunc("POST /api/worktrees/open", s.handleOpenWorktree)
	return s.localhostGuard(mux)
}

// @spec WEB-UI-001, WEB-UI-002, WEB-UI-003, WEB-UI-004, WEB-UI-005, WEB-UI-006, WEB-UI-007, WEB-UI-008, WEB-UI-009
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		writeJSONError(w, http.StatusNotFound, "not found", "")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(embeddedUI)
}

// @spec WEB-API-002
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.Version,
		"pid":     os.Getpid(),
		"port":    s.Port,
	})
}

// @spec WEB-API-003
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutting_down"})
	select {
	case s.ShutdownCh <- struct{}{}:
	default:
	}
}

// @spec WEB-API-004
func (s *Server) handleGetRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.Store.ListRepos()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

// @spec WEB-API-005
func (s *Server) handleDeleteRepos(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path         string `json:"path"`
		PruneMissing bool   `json:"prune_missing"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body", "")
		return
	}
	if req.PruneMissing {
		if err := s.Store.PruneMissingRepos(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeJSONError(w, http.StatusBadRequest, "path or prune_missing required", "")
		return
	}
	if err := s.Store.RemoveRepo(req.Path); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// @spec WEB-API-006
func (s *Server) handleRepoDetails(w http.ResponseWriter, r *http.Request) {
	repoPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if repoPath == "" || !IsRepoHealthy(repoPath) {
		writeJSON(w, http.StatusOK, map[string]any{
			"exists":        false,
			"is_local_only": false,
			"remotes":       []Remote{},
			"worktrees":     []Worktree{},
			"branches":      []Branch{},
			"remote_refs":   []string{},
		})
		return
	}

	wts, err := ListWorktrees(repoPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list worktrees", err.Error())
		return
	}
	branches, err := ListBranches(repoPath, wts)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list branches", err.Error())
		return
	}
	remotes, err := ListRemotes(repoPath)
	if err != nil {
		remotes = []Remote{}
	}
	remoteRefs, err := ListRemoteRefs(repoPath)
	if err != nil {
		remoteRefs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exists":        true,
		"is_local_only": len(remotes) == 0,
		"remotes":       remotes,
		"worktrees":     wts,
		"branches":      branches,
		"remote_refs":   remoteRefs,
	})
}

// @spec WEB-API-013
func (s *Server) handleRepoRemoteItems(w http.ResponseWriter, r *http.Request) {
	repoPath := strings.TrimSpace(r.URL.Query().Get("path"))
	remoteName := strings.TrimSpace(r.URL.Query().Get("remote"))
	if repoPath == "" || !IsRepoHealthy(repoPath) {
		writeJSON(w, http.StatusOK, map[string]any{
			"is_local_only": true,
			"remote":        "",
			"issues":        []IssueItem{},
			"prs":           []PRItem{},
		})
		return
	}
	remotes, _ := ListRemotes(repoPath)
	if len(remotes) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"is_local_only": true,
			"remote":        "",
			"issues":        []IssueItem{},
			"prs":           []PRItem{},
		})
		return
	}
	resolvedRemote, issues, prs, err := ListRemoteIssuesAndPRs(repoPath, remoteName)
	resp := map[string]any{
		"is_local_only": false,
		"remote":        resolvedRemote,
		"issues":        issues,
		"prs":           prs,
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// @spec WEB-API-014
func (s *Server) handleRepoFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath string `json:"repo_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.RepoPath) == "" {
		writeJSONError(w, http.StatusBadRequest, "repo_path is required", "")
		return
	}
	if !IsRepoHealthy(req.RepoPath) {
		writeJSONError(w, http.StatusBadRequest, "repository path is invalid or unreachable", "")
		return
	}
	if err := FetchRepo(req.RepoPath); err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to fetch remotes", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// @spec WEB-API-007
func (s *Server) handleSetUpstream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath string `json:"repo_path"`
		Branch   string `json:"branch"`
		Upstream string `json:"upstream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RepoPath == "" || req.Branch == "" {
		writeJSONError(w, http.StatusBadRequest, "repo_path and branch are required", "")
		return
	}
	if err := SetBranchUpstream(req.RepoPath, req.Branch, req.Upstream); err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to update upstream", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// @spec WEB-API-008
func (s *Server) handleDeleteBranch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath string `json:"repo_path"`
		Branch   string `json:"branch"`
		Force    bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RepoPath == "" || req.Branch == "" {
		writeJSONError(w, http.StatusBadRequest, "repo_path and branch are required", "")
		return
	}
	if err := DeleteBranch(req.RepoPath, req.Branch, req.Force); err != nil {
		if errors.Is(err, ErrNotFullyMerged) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"branch_deleted": false,
				"requires_force": true,
				"git_error":      err.Error(),
			})
			return
		}
		writeJSONError(w, http.StatusBadRequest, "failed to delete branch", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"branch_deleted": true})
}

// @spec WEB-API-009
func (s *Server) handleDeleteWorktree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath      string `json:"repo_path"`
		WorktreePath  string `json:"worktree_path"`
		Branch        string `json:"branch"`
		DeleteBranch  bool   `json:"delete_branch"`
		Force         bool   `json:"force"`
		ForceWorktree bool   `json:"force_worktree"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RepoPath == "" || req.WorktreePath == "" {
		writeJSONError(w, http.StatusBadRequest, "repo_path and worktree_path are required", "")
		return
	}
	res, err := DeleteWorktree(req.RepoPath, req.WorktreePath, req.Branch, req.DeleteBranch, req.Force, req.ForceWorktree)
	if err != nil {
		if errors.Is(err, ErrNotFullyMerged) && res.WorktreeRemoved {
			writeJSON(w, http.StatusConflict, res)
			return
		}
		writeJSONError(w, http.StatusBadRequest, "failed to remove worktree", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// @spec WEB-API-010
func (s *Server) handleGetEditors(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Store.LoadConfig()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"editors": s.ResolveEditors(cfg)})
}

// @spec WEB-API-011
func (s *Server) handlePutEditors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Overrides map[string]string `json:"overrides"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body", "")
		return
	}
	if err := s.Store.SaveEditorOverrides(req.Overrides); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	cfg, _ := s.Store.LoadConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"editors": s.ResolveEditors(cfg),
	})
}

// @spec WEB-API-012
func (s *Server) handleOpenWorktree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EditorID     string `json:"editor_id"`
		WorktreePath string `json:"worktree_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EditorID == "" {
		writeJSONError(w, http.StatusBadRequest, "editor_id is required", "")
		return
	}
	cfg, err := s.Store.LoadConfig()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if _, _, err := s.LaunchEditor(cfg, req.EditorID, req.WorktreePath); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "launched"})
}

func (s *Server) expandHome(p string) string {
	home := ""
	if s.EditorEnv != nil && s.EditorEnv.HomeDir != "" {
		home = s.EditorEnv.HomeDir
	} else if s.Store != nil && s.Store.HomeDir != "" {
		home = s.Store.HomeDir
	}
	if strings.HasPrefix(p, "~/") && home != "" {
		return filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	if p == "~" && home != "" {
		return home
	}
	return p
}

// @spec WEB-EDIT-001
func (s *Server) ResolveEditors(cfg Config) []EditorInfo {
	env := s.EditorEnv
	if env == nil {
		env = defaultEditorResolver(s.Store)
	}
	out := make([]EditorInfo, 0, len(supportedEditors))
	for _, spec := range supportedEditors {
		override := ""
		if cfg.EditorOverrides != nil {
			override = strings.TrimSpace(cfg.EditorOverrides[spec.ID])
		}
		detected := s.detectEditorTarget(env, spec)
		effective := ""
		if override != "" {
			exp := s.expandHome(override)
			if _, err := os.Stat(exp); err == nil {
				effective = exp
			}
		}
		if effective == "" {
			effective = detected
		}
		out = append(out, EditorInfo{
			ID:           spec.ID,
			Label:        spec.Label,
			DetectedPath: detected,
			OverridePath: override,
			Available:    effective != "",
		})
	}
	return out
}

func (s *Server) detectEditorTarget(env *EditorResolver, spec editorSpec) string {
	if env.LookPath != nil {
		for _, bin := range spec.Binaries {
			if p, err := env.LookPath(bin); err == nil && p != "" {
				return p
			}
		}
	}
	extraList := spec.ExtraPaths
	if env.ExtraPaths != nil {
		if custom, ok := env.ExtraPaths[spec.ID]; ok {
			extraList = custom
		}
	}
	for _, ep := range extraList {
		expanded := s.expandHome(ep)
		if info, err := os.Stat(expanded); err == nil && !info.IsDir() {
			return expanded
		}
	}
	if env.GOOS == "darwin" {
		appDirs := env.AppDirs
		if len(appDirs) == 0 {
			appDirs = []string{"/Applications"}
		}
		for _, dir := range appDirs {
			for _, appName := range spec.MacApps {
				candidate := filepath.Join(dir, appName+".app")
				if info, err := os.Stat(candidate); err == nil && info.IsDir() {
					return candidate
				}
			}
		}
	}
	return ""
}

// @spec WEB-EDIT-002
func (s *Server) LaunchEditor(cfg Config, editorID, worktreePath string) (string, []string, error) {
	env := s.EditorEnv
	if env == nil {
		env = defaultEditorResolver(s.Store)
	}

	targetDir := strings.TrimSpace(worktreePath)
	if targetDir == "" || targetDir == "~" {
		targetDir = s.expandHome("~")
	} else {
		targetDir = s.expandHome(targetDir)
	}
	if info, err := os.Stat(targetDir); err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("worktree directory does not exist: %s", targetDir)
	}

	var resolved string
	for _, ed := range s.ResolveEditors(cfg) {
		if ed.ID == editorID {
			if ed.OverridePath != "" {
				exp := s.expandHome(ed.OverridePath)
				if _, err := os.Stat(exp); err == nil {
					resolved = exp
				}
			}
			if resolved == "" {
				resolved = ed.DetectedPath
			}
			break
		}
	}
	if resolved == "" {
		return "", nil, fmt.Errorf("editor %q is not available", editorID)
	}

	var cmdName string
	var cmdArgs []string
	if env.GOOS == "darwin" && strings.HasSuffix(resolved, ".app") {
		cmdName = "open"
		cmdArgs = []string{"-a", resolved, targetDir}
	} else {
		cmdName = resolved
		cmdArgs = []string{targetDir}
	}

	if err := env.ExecStart(cmdName, cmdArgs...); err != nil {
		return "", nil, err
	}
	return cmdName, cmdArgs, nil
}
