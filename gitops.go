package main

import (
	"bytes"
	"context"
	"errors"
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
