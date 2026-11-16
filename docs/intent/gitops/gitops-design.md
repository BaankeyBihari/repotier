---
parent: high-level-design
prefix: GITOPS
---

# Git Domain Operations (`GITOPS`)

## Context and Design Philosophy

The `GITOPS` segment encapsulates all interactions with the local `git` CLI (`os/exec`). Every operation runs `git -C <path> ...` bounded by a `10s` `context.WithTimeout` so user SSH agents, credential helpers, and `.gitconfig` settings are respected without risking hung subprocesses or in-memory state drift. `GITOPS` enforces Git's native worktree and branch safety model rather than arbitrary branch-name restrictions.

## Operations & Parsing Contracts

### 1. Canonical Repository Discovery & Health Check

- **Discovery (`DiscoverRepoRoot(cwd string) (string, error)`):**
  - Runs `git -C <cwd> rev-parse --path-format=absolute --git-common-dir`.
  - Checks `git -C <cwd> rev-parse --is-bare-repository`; if `true`, uses the `--git-common-dir` path directly, otherwise strips trailing `/.git` via `filepath.Dir(commonDir)` when `filepath.Base(commonDir) == ".git"`.
  - Resolves symlinks via `filepath.EvalSymlinks` so invoking `repotier` from either the primary working tree or any linked worktree resolves to the same canonical main repository root path.
- **Health Check (`IsRepoHealthy(repoPath string) bool`):**
  - Verifies `os.Stat(repoPath)` is an existing directory and `git -C <repoPath> rev-parse --git-common-dir` exits `0`.

### 2. Worktree Enumeration (`ListWorktrees(repoPath string)`)

- Runs `git -C <repoPath> worktree list --porcelain`.
- Parses stanza blocks separated by blank lines:
  - `worktree <path>` → worktree filesystem path (resolved via `filepath.EvalSymlinks` when matching against branch checkout paths).
  - `HEAD <sha>` → commit SHA (shortened to 7 chars for display or full SHA).
  - `branch refs/heads/<name>` → checked-out local branch name.
  - `detached` → sets `branch` to `""` (`detached: true`).
- **Primary Worktree Rule:** The first stanza returned by `git worktree list --porcelain` is always marked `is_main: true`; all subsequent stanzas are marked `is_main: false`.
- **Dirty Count:** For each worktree whose path exists on disk, runs `git -C <wt_path> status --porcelain` and counts non-empty output lines (`dirty_count`).

### 3. Local Branches & Worktree Cross-Reference (`ListBranches(repoPath string, worktrees []Worktree)`)

- Runs `git -C <repoPath> for-each-ref --format=%(refname:short)|%(upstream:short)|%(upstream:track)|%(HEAD) refs/heads/`.
- For each branch:
  - `name`: `%(refname:short)`
  - `upstream`: `%(upstream:short)` (empty if no upstream configured)
  - `track_status`: `%(upstream:track)` (e.g., `[ahead 2, behind 1]`, `[ahead 1]`, `[gone]`, or `""`), plus parsed `ahead` / `behind` integer counts (`ahead: 0, behind: 0, gone: true` when `[gone]` is present).
  - Cross-references against `worktrees`:
    - `checked_out_in`: matching worktree `path` where `wt.Branch == branch.Name` (`""` if not checked out in any worktree).
    - `is_main_wt`: `true` if and only if the matching worktree has `is_main == true`.

### 4. Upstream & Remote Management

- **Configured Remotes (`ListRemotes(repoPath string)`):**
  - Runs `git -C <repoPath> remote -v`.
  - Deduplicates remote names (preferring `(fetch)` URL), normalizes SSH (`git@github.com:owner/repo.git`, `ssh://git@github.com/owner/repo.git`) and HTTPS (`https://github.com/owner/repo.git`) URLs into a browser-clickable `web_url` (`https://github.com/owner/repo`) and `repo_slug` (`owner/repo`), and returns `[]Remote` (`name`, `url`, `web_url`, `repo_slug`).
  - When `len(remotes) == 0`, the repository is classified as **local-only** (`is_local_only: true`).
- **Read-Only Remote Issues & Pull Requests (`ListRemoteIssuesAndPRs(repoPath, remoteName string)`):**
  - Resolves target remote (`origin` by default, or first remote / requested `remoteName`). If the repository has zero remotes (`is_local_only`), returns empty slices immediately.
  - Invokes `gh issue list -R <slug_or_url> --state open --limit 30 --json number,title,url,state,author,updatedAt` and `gh pr list -R <slug_or_url> --state open --limit 30 --json number,title,url,state,headRefName,baseRefName,isDraft,author,updatedAt` (`10s` timeout).
  - If `gh` is not installed or fails and `repo_slug` is a GitHub `owner/repo`, falls back to unauthenticated `GET https://api.github.com/repos/<owner>/<repo>/issues?state=open&per_page=30` (filtering out items with a `pull_request` key) and `GET https://api.github.com/repos/<owner>/<repo>/pulls?state=open&per_page=30`.
- **Available Remote Refs (`ListRemoteRefs(repoPath string)`):**
  - Runs `git -C <repoPath> for-each-ref --format=%(refname:short) refs/remotes/`.
  - Filters out symbolic HEAD refs (`*/HEAD` or bare remote names without a `/` branch component).
- **Set / Unset Upstream (`SetBranchUpstream(repoPath, branch, upstream string)`):**
  - Non-empty `upstream`: runs `git -C <repoPath> branch --set-upstream-to=<upstream> <branch>`.
  - Empty `upstream`: runs `git -C <repoPath> branch --unset-upstream <branch>`.

### 5. Branch & Linked Worktree Deletion Rules

| Target State | Allowed Action | Git Command Sequence |
|---|---|---|
| Branch checked out in any worktree (`checked_out_in != ""`) via `DeleteBranch` | **Forbidden on `DeleteBranch`** | Rejects before calling `git branch -d/-D` (primary worktree is permanently forbidden; linked-worktree branches must be deleted via `DeleteWorktree` so the worktree is removed first). |
| Unattached branch (`checked_out_in == ""`) via `DeleteBranch` | Direct branch delete | `git -C <repoPath> branch -d <branch>` (or `-D` when `force == true`). |
| Linked worktree (`is_main == false`), `delete_branch == false` or `branch == ""` (detached `HEAD`) | Worktree remove only | `git -C <repoPath> worktree remove [--force] <wt_path>`. (When `branch == ""`, `delete_branch: true` is treated as a no-op and returns `worktree_removed: true, branch_deleted: false`.) |
| Linked worktree (`is_main == false`), `delete_branch == true` and `branch != ""` | Worktree remove then branch delete | 1. `git -C <repoPath> worktree remove [--force] <wt_path>`<br>2. `git -C <repoPath> branch -d/-D <branch>` |

- **Structured Deletion Error Types:**
  - **Dirty Worktree (`ErrWorktreeDirty`):** When `force_worktree == false` and `git worktree remove <wt_path>` fails because the worktree contains modified or untracked files, returns `ErrWorktreeDirty` carrying `git_error` stderr.
  - **Unmerged Branch (`ErrNotFullyMerged`):** When `force == false` and `git branch -d <branch>` exits non-zero with stderr containing `"not fully merged"`, returns `ErrNotFullyMerged` carrying `git_error` so the caller can return HTTP `409 Conflict` with `"requires_force": true`.
  - When deleting a linked worktree with `delete_branch: true`, if `git worktree remove` succeeds and `git branch -d` fails with `ErrNotFullyMerged`, the result records `worktree_removed: true, branch_deleted: false, requires_force: true`.

## Decisions & Alternatives

| Decision | Chosen | Alternatives Considered | Rationale |
|---|---|---|---|
| **Git Execution Layer** | Subprocess `os/exec` (`git -C <path>`) with `10s` `context.WithTimeout` | `go-git` pure-Go library | `go-git` has incomplete support for `git worktree` administrative files and custom `.gitconfig` extensions; `git` CLI is canonical and timeout prevents hangs on stale `.git/index.lock`. |
| **Main Worktree Identification** | Index `0` of `git worktree list --porcelain` | Checking whether `.git` is a directory vs. file | Git guarantees the main worktree is emitted first in porcelain output, even when run from inside a linked worktree. |
| **Canonical Root Resolution** | `git rev-parse --path-format=absolute --git-common-dir` + `filepath.EvalSymlinks` | `git rev-parse --show-toplevel` | `--show-toplevel` returns the linked worktree's directory when run inside a linked worktree, whereas `--git-common-dir` always points to the main repository's `.git` dir. |

## Open Questions & Future Decisions

### Resolved

1. ✅ **No Hardcoded Branch Protection:** Only `is_main_wt == true` blocks branch deletion.
2. ✅ **Dirty Worktree Error:** Distinct `ErrWorktreeDirty` when `git worktree remove` fails without `--force`.
3. ✅ **Detached HEAD Worktree Deletion:** Ignore `delete_branch: true` when `branch == ""` (`worktree_removed: true, branch_deleted: false`).
4. ✅ **Subprocess Timeout & `[gone]` Parsing:** Bound `git` commands with `10s` timeout; parse `[gone]` as `ahead: 0, behind: 0, gone: true`.

### Deferred

1. Handling pruned/stale worktree administrative entries (`git worktree prune`) when a linked worktree folder was manually deleted from Finder/terminal.

## References

- Parent HLD: [high-level-design.md](../../high-level-design.md)
- EARS Specs: [gitops-specs.md](gitops-specs.md)
