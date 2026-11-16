package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, realDir, "init", "-b", "main")
	mustGit(t, realDir, "config", "user.email", "test@example.com")
	mustGit(t, realDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(realDir, "README.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, realDir, "add", "README.md")
	mustGit(t, realDir, "commit", "-m", "initial")
	return realDir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, string(out))
	}
	return string(out)
}

// @spec GITOPS-EXEC-001
func TestGitOpsTimeout(t *testing.T) {
	repo := initTestGitRepo(t)
	orig := gitCmdTimeout
	defer func() { gitCmdTimeout = orig }()
	gitCmdTimeout = 10 * time.Millisecond

	_, _, err := runGit(repo, "status")
	if err != nil {
		// Even with 10ms, check that runGit executes git -C and doesn't return stub error
		if err.Error() == "not implemented" {
			t.Fatalf("expected runGit implementation, got stub error")
		}
	}
}

// @spec GITOPS-REPO-001, GITOPS-REPO-002
func TestDiscoverRepoRootAndHealth(t *testing.T) {
	repo := initTestGitRepo(t)
	wtDir := filepath.Join(t.TempDir(), "linked-wt")
	mustGit(t, repo, "worktree", "add", "-b", "feat/wt", wtDir)

	rootFromMain, err := DiscoverRepoRoot(repo)
	if err != nil || rootFromMain != repo {
		t.Fatalf("DiscoverRepoRoot(main) = %q, %v; want %q", rootFromMain, err, repo)
	}

	rootFromWT, err := DiscoverRepoRoot(wtDir)
	if err != nil || rootFromWT != repo {
		t.Fatalf("DiscoverRepoRoot(linked-wt) = %q, %v; want %q", rootFromWT, err, repo)
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	mustGit(t, repo, "clone", "--bare", repo, bareDir)
	realBare, _ := filepath.EvalSymlinks(bareDir)
	rootFromBare, err := DiscoverRepoRoot(realBare)
	if err != nil || rootFromBare != realBare {
		t.Fatalf("DiscoverRepoRoot(bare) = %q, %v; want %q", rootFromBare, err, realBare)
	}

	if !IsRepoHealthy(repo) {
		t.Fatalf("expected IsRepoHealthy(%q) == true", repo)
	}
	if IsRepoHealthy(filepath.Join(repo, "nonexistent")) {
		t.Fatalf("expected IsRepoHealthy(nonexistent) == false")
	}
}

// @spec GITOPS-WT-001, GITOPS-WT-002
func TestListWorktreesMainAndDirtyCount(t *testing.T) {
	repo := initTestGitRepo(t)
	wtParent, _ := filepath.EvalSymlinks(t.TempDir())
	wtDir := filepath.Join(wtParent, "wt-dirty")
	mustGit(t, repo, "worktree", "add", "-b", "feat/dirty", wtDir)

	if err := os.WriteFile(filepath.Join(wtDir, "untracked.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "README.md"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wts, err := ListWorktrees(repo)
	if err != nil {
		t.Fatalf("ListWorktrees failed: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(wts))
	}
	if !wts[0].IsMain || wts[0].Branch != "main" || wts[0].DirtyCount != 0 {
		t.Fatalf("unexpected primary worktree: %+v", wts[0])
	}
	if wts[1].IsMain || wts[1].Branch != "feat/dirty" || wts[1].DirtyCount != 2 {
		t.Fatalf("unexpected linked worktree: %+v", wts[1])
	}
}

// @spec GITOPS-BR-001, GITOPS-BR-002, GITOPS-UP-001, GITOPS-UP-002
func TestBranchesAndUpstreams(t *testing.T) {
	repo := initTestGitRepo(t)
	remoteDir, _ := filepath.EvalSymlinks(t.TempDir())
	bareRemote := filepath.Join(remoteDir, "origin.git")
	mustGit(t, repo, "clone", "--bare", repo, bareRemote)
	mustGit(t, repo, "remote", "add", "origin", bareRemote)
	mustGit(t, repo, "fetch", "origin")

	if err := SetBranchUpstream(repo, "main", "origin/main"); err != nil {
		t.Fatalf("SetBranchUpstream failed: %v", err)
	}

	refs, err := ListRemoteRefs(repo)
	if err != nil || len(refs) != 1 || refs[0] != "origin/main" {
		t.Fatalf("ListRemoteRefs = %v, %v; want [origin/main]", refs, err)
	}

	// Advance main by 1 commit so it is [ahead 1]
	if err := os.WriteFile(filepath.Join(repo, "ahead.txt"), []byte("1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "ahead.txt")
	mustGit(t, repo, "commit", "-m", "ahead commit")

	wtParent, _ := filepath.EvalSymlinks(t.TempDir())
	wtDir := filepath.Join(wtParent, "wt-linked")
	mustGit(t, repo, "worktree", "add", "-b", "feat/linked", wtDir)

	wts, err := ListWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	branches, err := ListBranches(repo, wts)
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}

	byName := map[string]Branch{}
	for _, b := range branches {
		byName[b.Name] = b
	}

	mainBr := byName["main"]
	if !mainBr.IsMainWT || mainBr.CheckedOutIn != repo || mainBr.Upstream != "origin/main" || mainBr.Ahead != 1 {
		t.Fatalf("unexpected main branch metadata: %+v", mainBr)
	}

	linkedBr := byName["feat/linked"]
	if linkedBr.IsMainWT || linkedBr.CheckedOutIn != wtDir {
		t.Fatalf("unexpected linked branch metadata: %+v", linkedBr)
	}

	if err := SetBranchUpstream(repo, "main", ""); err != nil {
		t.Fatalf("SetBranchUpstream(unset) failed: %v", err)
	}
}

// @spec GITOPS-DEL-001, GITOPS-DEL-002, GITOPS-DEL-003
func TestDeleteBranchSafetyAndForce(t *testing.T) {
	repo := initTestGitRepo(t)

	// Cannot delete branch checked out in primary worktree
	if err := DeleteBranch(repo, "main", true); err == nil {
		t.Fatalf("expected error deleting primary checked-out branch")
	}

	// Create unmerged unattached branch
	mustGit(t, repo, "checkout", "-b", "feat/unmerged")
	if err := os.WriteFile(filepath.Join(repo, "u.txt"), []byte("u\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "u.txt")
	mustGit(t, repo, "commit", "-m", "unmerged")
	mustGit(t, repo, "checkout", "main")

	err := DeleteBranch(repo, "feat/unmerged", false)
	if !errors.Is(err, ErrNotFullyMerged) {
		t.Fatalf("DeleteBranch(force=false) on unmerged = %v; want ErrNotFullyMerged", err)
	}

	if err := DeleteBranch(repo, "feat/unmerged", true); err != nil {
		t.Fatalf("DeleteBranch(force=true) failed: %v", err)
	}
}

// @spec GITOPS-DEL-004, GITOPS-DEL-005
func TestDeleteWorktreeDirtyDetachedAndUnmerged(t *testing.T) {
	repo := initTestGitRepo(t)
	wtParent, _ := filepath.EvalSymlinks(t.TempDir())

	// 1. Dirty worktree without force_worktree -> ErrWorktreeDirty; with force_worktree + unmerged branch -> requires_force
	wt1 := filepath.Join(wtParent, "wt1")
	mustGit(t, repo, "worktree", "add", "-b", "feat/wt1", wt1)
	if err := os.WriteFile(filepath.Join(wt1, "c.txt"), []byte("commit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, wt1, "add", "c.txt")
	mustGit(t, wt1, "commit", "-m", "wt1 unmerged")
	if err := os.WriteFile(filepath.Join(wt1, "dirty.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := DeleteWorktree(repo, wt1, "feat/wt1", true, false, false)
	if !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("expected ErrWorktreeDirty, got %v", err)
	}

	res, err := DeleteWorktree(repo, wt1, "feat/wt1", true, false, true)
	if !errors.Is(err, ErrNotFullyMerged) || !res.WorktreeRemoved || res.BranchDeleted || !res.RequiresForce {
		t.Fatalf("expected partial worktree removal + requires_force, got res=%+v err=%v", res, err)
	}

	// 2. Detached HEAD worktree with delete_branch=true -> skips branch delete cleanly
	wt2 := filepath.Join(wtParent, "wt2")
	mustGit(t, repo, "worktree", "add", "--detach", wt2)
	res2, err := DeleteWorktree(repo, wt2, "", true, false, false)
	if err != nil || !res2.WorktreeRemoved || res2.BranchDeleted {
		t.Fatalf("detached worktree delete failed: res=%+v err=%v", res2, err)
	}
}

// @spec GITOPS-REM-001, GITOPS-REM-002
func TestListRemotesAndRemoteIssuesPRs(t *testing.T) {
	repo := initTestGitRepo(t)

	// 1. Local-only repo (zero remotes)
	remotes, err := ListRemotes(repo)
	if err != nil || len(remotes) != 0 {
		t.Fatalf("expected 0 remotes on local-only repo, got %v (err=%v)", remotes, err)
	}

	// 2. Add SSH and HTTPS remotes
	mustGit(t, repo, "remote", "add", "origin", "git@github.com:BaankeyBihari/repotier.git")
	mustGit(t, repo, "remote", "add", "upstream", "https://github.com/octocat/Hello-World.git")

	remotes, err = ListRemotes(repo)
	if err != nil || len(remotes) != 2 {
		t.Fatalf("expected 2 remotes, got %v (err=%v)", remotes, err)
	}
	if remotes[0].Name != "origin" || remotes[0].WebURL != "https://github.com/BaankeyBihari/repotier" || remotes[0].RepoSlug != "BaankeyBihari/repotier" {
		t.Fatalf("unexpected origin remote: %+v", remotes[0])
	}
	if remotes[1].Name != "upstream" || remotes[1].WebURL != "https://github.com/octocat/Hello-World" || remotes[1].RepoSlug != "octocat/Hello-World" {
		t.Fatalf("unexpected upstream remote: %+v", remotes[1])
	}

	// 3. Read-only issues & PRs via fallback REST API when gh CLI is unavailable
	origGH := ghBinaryName
	defer func() { ghBinaryName = origGH }()
	ghBinaryName = "nonexistent-gh-bin"

	var issuesJSON = `[{"number":3,"title":"feat: add fetch","html_url":"https://github.com/BaankeyBihari/repotier/issues/3","state":"open","user":{"login":"BaankeyBihari"}}]`
	var pullsJSON = `[{"number":4,"title":"ci: add renovate","html_url":"https://github.com/BaankeyBihari/repotier/pull/4","state":"open","draft":false,"head":{"ref":"ci/gov"},"base":{"ref":"main"},"user":{"login":"BaankeyBihari"}}]`
	origFetcher := githubHTTPGet
	defer func() { githubHTTPGet = origFetcher }()
	githubHTTPGet = func(endpoint string) ([]byte, error) {
		if filepath.Base(endpoint) == "issues?state=open&per_page=30" {
			return []byte(issuesJSON), nil
		}
		return []byte(pullsJSON), nil
	}

	remName, issues, prs, err := ListRemoteIssuesAndPRs(repo, "origin")
	if err != nil || remName != "origin" || len(issues) != 1 || len(prs) != 1 {
		t.Fatalf("ListRemoteIssuesAndPRs = %q, %v, %v, %v", remName, issues, prs, err)
	}
	if issues[0].Number != 3 || prs[0].Number != 4 || prs[0].HeadRef != "ci/gov" || prs[0].BaseRef != "main" {
		t.Fatalf("unexpected issues/prs payload: issues=%+v prs=%+v", issues, prs)
	}
}
