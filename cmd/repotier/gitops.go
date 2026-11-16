package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFullyMerged    = errors.New("branch is not fully merged")
	ErrWorktreeDirty     = errors.New("worktree contains modified or untracked files")
	ErrProtectedCheckout = errors.New("cannot delete checked-out branch or primary worktree")
	gitCmdTimeout        = 10 * time.Second
	reAhead              = regexp.MustCompile(`ahead (\d+)`)
	reBehind             = regexp.MustCompile(`behind (\d+)`)
)

type GitError struct {
	Kind   error
	Stderr string
}

func (e *GitError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return e.Kind.Error()
}

func (e *GitError) Unwrap() error {
	return e.Kind
}

type Worktree struct {
	Path       string `json:"path"`
	Head       string `json:"head"`
	Branch     string `json:"branch"`
	Detached   bool   `json:"detached"`
	IsMain     bool   `json:"is_main"`
	DirtyCount int    `json:"dirty_count"`
}

type Branch struct {
	Name         string `json:"name"`
	Upstream     string `json:"upstream"`
	TrackStatus  string `json:"track_status"`
	Ahead        int    `json:"ahead"`
	Behind       int    `json:"behind"`
	Gone         bool   `json:"gone"`
	CheckedOutIn string `json:"checked_out_in"`
	IsMainWT     bool   `json:"is_main_wt"`
}

type DeleteWorktreeResult struct {
	WorktreeRemoved bool   `json:"worktree_removed"`
	BranchDeleted   bool   `json:"branch_deleted"`
	RequiresForce   bool   `json:"requires_force,omitempty"`
	GitError        string `json:"git_error,omitempty"`
}

type Remote struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	WebURL   string `json:"web_url"`
	RepoSlug string `json:"repo_slug"`
}

type IssueItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Author string `json:"author"`
}

type PRItem struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	State   string `json:"state"`
	HeadRef string `json:"head_ref"`
	BaseRef string `json:"base_ref"`
	IsDraft bool   `json:"is_draft"`
	Author  string `json:"author"`
}

var (
	githubAPIBaseURL = "https://api.github.com"
	ghBinaryName     = "gh"
	githubHTTPGet    = defaultGitHubHTTPGet
)

func defaultGitHubHTTPGet(endpoint string) ([]byte, error) {
	client := &http.Client{Timeout: gitCmdTimeout}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "repotier")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func normalizeGitRemoteURL(rawURL string) (string, string) {
	u := strings.TrimSpace(rawURL)
	u = strings.TrimSuffix(u, ".git")
	if strings.HasPrefix(u, "git@") {
		rest := strings.TrimPrefix(u, "git@")
		if parts := strings.SplitN(rest, ":", 2); len(parts) == 2 {
			host := parts[0]
			slug := strings.TrimPrefix(parts[1], "/")
			return "https://" + host + "/" + slug, slug
		}
	}
	if strings.HasPrefix(u, "ssh://") {
		rest := strings.TrimPrefix(u, "ssh://")
		rest = strings.TrimPrefix(rest, "git@")
		if parts := strings.SplitN(rest, "/", 2); len(parts) == 2 {
			host := strings.SplitN(parts[0], ":", 2)[0]
			slug := parts[1]
			return "https://" + host + "/" + slug, slug
		}
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		schemeIdx := strings.Index(u, "://")
		rest := u[schemeIdx+3:]
		if parts := strings.SplitN(rest, "/", 2); len(parts) == 2 {
			return u, parts[1]
		}
		return u, ""
	}
	return "", ""
}

// @spec GITOPS-REM-001
func ListRemotes(repoPath string) ([]Remote, error) {
	out, stderr, err := runGit(repoPath, "remote", "-v")
	if err != nil {
		return nil, &GitError{Kind: err, Stderr: stderr}
	}
	seen := make(map[string]bool)
	remotes := []Remote{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, rawURL := fields[0], fields[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		webURL, slug := normalizeGitRemoteURL(rawURL)
		remotes = append(remotes, Remote{
			Name:     name,
			URL:      rawURL,
			WebURL:   webURL,
			RepoSlug: slug,
		})
	}
	return remotes, nil
}

// @spec GITOPS-REM-002
func ListRemoteIssuesAndPRs(repoPath, remoteName string) (string, []IssueItem, []PRItem, error) {
	remotes, err := ListRemotes(repoPath)
	if err != nil || len(remotes) == 0 {
		return "", []IssueItem{}, []PRItem{}, err
	}

	selected := remotes[0]
	for _, r := range remotes {
		if (remoteName != "" && r.Name == remoteName) || (remoteName == "" && r.Name == "origin") {
			selected = r
			break
		}
	}

	targetRepo := selected.WebURL
	if targetRepo == "" {
		targetRepo = selected.RepoSlug
	}
	if targetRepo == "" {
		return selected.Name, []IssueItem{}, []PRItem{}, nil
	}

	if issues, prs, err := fetchViaGHCLI(repoPath, targetRepo); err == nil {
		return selected.Name, issues, prs, nil
	}
	if selected.RepoSlug != "" {
		issues, prs, err := fetchViaGitHubREST(selected.RepoSlug)
		return selected.Name, issues, prs, err
	}
	return selected.Name, []IssueItem{}, []PRItem{}, nil
}

func fetchViaGHCLI(repoPath, targetRepo string) ([]IssueItem, []PRItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	issueCmd := exec.CommandContext(ctx, ghBinaryName, "issue", "list", "-R", targetRepo, "--state", "open", "--limit", "30", "--json", "number,title,url,state,author")
	issueCmd.Dir = repoPath
	issueOut, err := issueCmd.Output()
	if err != nil {
		return nil, nil, err
	}

	prCmd := exec.CommandContext(ctx, ghBinaryName, "pr", "list", "-R", targetRepo, "--state", "open", "--limit", "30", "--json", "number,title,url,state,headRefName,baseRefName,isDraft,author")
	prCmd.Dir = repoPath
	prOut, err := prCmd.Output()
	if err != nil {
		return nil, nil, err
	}

	var rawIssues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := json.Unmarshal(issueOut, &rawIssues); err != nil {
		return nil, nil, err
	}
	issues := make([]IssueItem, 0, len(rawIssues))
	for _, it := range rawIssues {
		issues = append(issues, IssueItem{
			Number: it.Number,
			Title:  it.Title,
			URL:    it.URL,
			State:  strings.ToLower(it.State),
			Author: it.Author.Login,
		})
	}

	var rawPRs []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		State       string `json:"state"`
		HeadRefName string `json:"headRefName"`
		BaseRefName string `json:"baseRefName"`
		IsDraft     bool   `json:"isDraft"`
		Author      struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := json.Unmarshal(prOut, &rawPRs); err != nil {
		return nil, nil, err
	}
	prs := make([]PRItem, 0, len(rawPRs))
	for _, pr := range rawPRs {
		prs = append(prs, PRItem{
			Number:  pr.Number,
			Title:   pr.Title,
			URL:     pr.URL,
			State:   strings.ToLower(pr.State),
			HeadRef: pr.HeadRefName,
			BaseRef: pr.BaseRefName,
			IsDraft: pr.IsDraft,
			Author:  pr.Author.Login,
		})
	}
	return issues, prs, nil
}

func fetchViaGitHubREST(repoSlug string) ([]IssueItem, []PRItem, error) {
	issuesBody, err := githubHTTPGet(fmt.Sprintf("%s/repos/%s/issues?state=open&per_page=30", githubAPIBaseURL, repoSlug))
	if err != nil {
		return []IssueItem{}, []PRItem{}, err
	}
	var ghIssues []struct {
		Number      int             `json:"number"`
		Title       string          `json:"title"`
		HTMLURL     string          `json:"html_url"`
		State       string          `json:"state"`
		PullRequest json.RawMessage `json:"pull_request"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(issuesBody, &ghIssues); err != nil {
		return []IssueItem{}, []PRItem{}, err
	}
	issues := []IssueItem{}
	for _, it := range ghIssues {
		if len(it.PullRequest) > 0 && string(it.PullRequest) != "null" {
			continue
		}
		issues = append(issues, IssueItem{
			Number: it.Number,
			Title:  it.Title,
			URL:    it.HTMLURL,
			State:  it.State,
			Author: it.User.Login,
		})
	}

	pullsBody, err := githubHTTPGet(fmt.Sprintf("%s/repos/%s/pulls?state=open&per_page=30", githubAPIBaseURL, repoSlug))
	if err != nil {
		return issues, []PRItem{}, err
	}
	var ghPulls []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Draft   bool   `json:"draft"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(pullsBody, &ghPulls); err != nil {
		return issues, []PRItem{}, err
	}
	prs := make([]PRItem, 0, len(ghPulls))
	for _, pr := range ghPulls {
		prs = append(prs, PRItem{
			Number:  pr.Number,
			Title:   pr.Title,
			URL:     pr.HTMLURL,
			State:   pr.State,
			HeadRef: pr.Head.Ref,
			BaseRef: pr.Base.Ref,
			IsDraft: pr.Draft,
			Author:  pr.User.Login,
		})
	}
	return issues, prs, nil
}

// @spec GITOPS-EXEC-001
func runGit(dir string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()

	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimRight(stdout.String(), "\r\n"), strings.TrimSpace(stderr.String()), err
}

// @spec GITOPS-REPO-001
func DiscoverRepoRoot(cwd string) (string, error) {
	commonDir, stderr, err := runGit(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", &GitError{Kind: err, Stderr: stderr}
	}
	isBareOut, _, err := runGit(cwd, "rev-parse", "--is-bare-repository")
	isBare := err == nil && strings.TrimSpace(isBareOut) == "true"

	root := strings.TrimSpace(commonDir)
	if !isBare && filepath.Base(root) == ".git" {
		root = filepath.Dir(root)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Clean(root), nil
}

// @spec GITOPS-REPO-002
func IsRepoHealthy(repoPath string) bool {
	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		return false
	}
	_, _, err = runGit(repoPath, "rev-parse", "--git-common-dir")
	return err == nil
}

// @spec GITOPS-WT-001, GITOPS-WT-002
func ListWorktrees(repoPath string) ([]Worktree, error) {
	out, stderr, err := runGit(repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, &GitError{Kind: err, Stderr: stderr}
	}

	var worktrees []Worktree
	blocks := strings.Split(strings.TrimSpace(out), "\n\n")
	for idx, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		wt := Worktree{IsMain: idx == 0}
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				p := strings.TrimPrefix(line, "worktree ")
				if resolved, err := filepath.EvalSymlinks(p); err == nil {
					p = resolved
				}
				wt.Path = filepath.Clean(p)
			case strings.HasPrefix(line, "HEAD "):
				wt.Head = strings.TrimPrefix(line, "HEAD ")
				if len(wt.Head) > 7 {
					wt.Head = wt.Head[:7]
				}
			case strings.HasPrefix(line, "branch "):
				ref := strings.TrimPrefix(line, "branch ")
				wt.Branch = strings.TrimPrefix(ref, "refs/heads/")
			case line == "detached":
				wt.Detached = true
				wt.Branch = ""
			}
		}
		if info, err := os.Stat(wt.Path); err == nil && info.IsDir() {
			statusOut, _, sErr := runGit(wt.Path, "status", "--porcelain")
			if sErr == nil && statusOut != "" {
				for _, sl := range strings.Split(statusOut, "\n") {
					if strings.TrimSpace(sl) != "" {
						wt.DirtyCount++
					}
				}
			}
		}
		worktrees = append(worktrees, wt)
	}
	return worktrees, nil
}

// @spec GITOPS-BR-001, GITOPS-BR-002
func ListBranches(repoPath string, worktrees []Worktree) ([]Branch, error) {
	out, stderr, err := runGit(repoPath, "for-each-ref", "--format=%(refname:short)|%(upstream:short)|%(upstream:track)|%(HEAD)", "refs/heads/")
	if err != nil {
		return nil, &GitError{Kind: err, Stderr: stderr}
	}

	wtByBranch := make(map[string]Worktree, len(worktrees))
	for _, wt := range worktrees {
		if wt.Branch != "" {
			wtByBranch[wt.Branch] = wt
		}
	}

	var branches []Branch
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		br := Branch{
			Name:        parts[0],
			Upstream:    parts[1],
			TrackStatus: parts[2],
		}
		if strings.Contains(br.TrackStatus, "gone") {
			br.Gone = true
		} else {
			if m := reAhead.FindStringSubmatch(br.TrackStatus); len(m) == 2 {
				br.Ahead, _ = strconv.Atoi(m[1])
			}
			if m := reBehind.FindStringSubmatch(br.TrackStatus); len(m) == 2 {
				br.Behind, _ = strconv.Atoi(m[1])
			}
		}
		if wt, ok := wtByBranch[br.Name]; ok {
			br.CheckedOutIn = wt.Path
			br.IsMainWT = wt.IsMain
		}
		branches = append(branches, br)
	}
	return branches, nil
}

// @spec GITOPS-UP-001
func ListRemoteRefs(repoPath string) ([]string, error) {
	out, stderr, err := runGit(repoPath, "for-each-ref", "--format=%(refname:short)", "refs/remotes/")
	if err != nil {
		return nil, &GitError{Kind: err, Stderr: stderr}
	}
	refs := []string{}
	for _, line := range strings.Split(out, "\n") {
		ref := strings.TrimSpace(line)
		if ref == "" || strings.HasSuffix(ref, "/HEAD") || !strings.Contains(ref, "/") {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// @spec GITOPS-UP-002
func SetBranchUpstream(repoPath, branch, upstream string) error {
	var stderr string
	var err error
	if strings.TrimSpace(upstream) == "" {
		_, stderr, err = runGit(repoPath, "branch", "--unset-upstream", branch)
	} else {
		_, stderr, err = runGit(repoPath, "branch", "--set-upstream-to="+upstream, branch)
	}
	if err != nil {
		return &GitError{Kind: err, Stderr: stderr}
	}
	return nil
}

// @spec GITOPS-DEL-001, GITOPS-DEL-002, GITOPS-DEL-003
func DeleteBranch(repoPath, branch string, force bool) error {
	wts, err := ListWorktrees(repoPath)
	if err != nil {
		return err
	}
	for _, wt := range wts {
		if wt.Branch == branch {
			return &GitError{Kind: ErrProtectedCheckout, Stderr: "branch is currently checked out in worktree " + wt.Path}
		}
	}
	return deleteBranchCmd(repoPath, branch, force)
}

func deleteBranchCmd(repoPath, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, stderr, err := runGit(repoPath, "branch", flag, branch)
	if err != nil {
		if !force && strings.Contains(strings.ToLower(stderr), "not fully merged") {
			return &GitError{Kind: ErrNotFullyMerged, Stderr: stderr}
		}
		return &GitError{Kind: err, Stderr: stderr}
	}
	return nil
}

// @spec GITOPS-DEL-001, GITOPS-DEL-004, GITOPS-DEL-005
func DeleteWorktree(repoPath, wtPath, branch string, deleteBranch, forceBranch, forceWorktree bool) (DeleteWorktreeResult, error) {
	cleanWT := filepath.Clean(wtPath)
	if resolved, err := filepath.EvalSymlinks(cleanWT); err == nil {
		cleanWT = filepath.Clean(resolved)
	}

	wts, err := ListWorktrees(repoPath)
	if err != nil {
		return DeleteWorktreeResult{}, err
	}
	for _, wt := range wts {
		if wt.Path == cleanWT && wt.IsMain {
			return DeleteWorktreeResult{}, &GitError{Kind: ErrProtectedCheckout, Stderr: "cannot remove primary repository worktree"}
		}
	}

	args := []string{"worktree", "remove"}
	if forceWorktree {
		args = append(args, "--force")
	}
	args = append(args, cleanWT)

	_, stderr, err := runGit(repoPath, args...)
	if err != nil {
		lower := strings.ToLower(stderr)
		if !forceWorktree && (strings.Contains(lower, "modified or untracked") || strings.Contains(lower, "use --force")) {
			return DeleteWorktreeResult{}, &GitError{Kind: ErrWorktreeDirty, Stderr: stderr}
		}
		return DeleteWorktreeResult{}, &GitError{Kind: err, Stderr: stderr}
	}

	res := DeleteWorktreeResult{WorktreeRemoved: true}
	if !deleteBranch || strings.TrimSpace(branch) == "" {
		return res, nil
	}

	if err := deleteBranchCmd(repoPath, branch, forceBranch); err != nil {
		var ge *GitError
		gitStderr := err.Error()
		if errors.As(err, &ge) && ge.Stderr != "" {
			gitStderr = ge.Stderr
		}
		if errors.Is(err, ErrNotFullyMerged) {
			res.RequiresForce = true
			res.GitError = gitStderr
			return res, err
		}
		res.GitError = gitStderr
		return res, err
	}

	res.BranchDeleted = true
	return res, nil
}
