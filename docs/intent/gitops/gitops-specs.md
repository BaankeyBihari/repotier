# Git Domain Operations Specs (`GITOPS`)

## Subprocess Execution & Repository Discovery

- [x] **GITOPS-EXEC-001**: The `GITOPS` layer shall execute all Git CLI operations as `git -C <path> ...` subprocesses bounded by a `10s` `context.WithTimeout`.
- [x] **GITOPS-REPO-001**: When `DiscoverRepoRoot(cwd)` is called inside a primary Git working tree, linked worktree, or bare repository, `GITOPS` shall execute `git -C <cwd> rev-parse --path-format=absolute --git-common-dir`, strip the trailing `/.git` segment when `git -C <cwd> rev-parse --is-bare-repository` is `false` and `filepath.Base(commonDir) == ".git"`, and resolve symlinks via `filepath.EvalSymlinks` to return the canonical main repository root path.
- [x] **GITOPS-REPO-002**: When `IsRepoHealthy(repoPath)` is called, `GITOPS` shall return `true` if and only if `os.Stat(repoPath)` is an existing directory and `git -C <repoPath> rev-parse --git-common-dir` exits with status `0`.

## Worktree Enumeration

- [x] **GITOPS-WT-001**: When `ListWorktrees(repoPath)` is called, `GITOPS` shall parse `git -C <repoPath> worktree list --porcelain` stanzas (`worktree`, `HEAD`, `branch refs/heads/<name>`, and `detached`), marking index `0` (the first stanza) with `is_main: true` and all subsequent stanzas with `is_main: false`.
- [x] **GITOPS-WT-002**: When `ListWorktrees(repoPath)` enumerates a worktree whose directory exists on disk, `GITOPS` shall execute `git -C <wt_path> status --porcelain` and set `dirty_count` to the number of non-empty output lines.

## Branch Listing & Worktree Cross-Reference

- [x] **GITOPS-BR-001**: When `ListBranches(repoPath, worktrees)` is called, `GITOPS` shall parse `git -C <repoPath> for-each-ref --format=%(refname:short)|%(upstream:short)|%(upstream:track)|%(HEAD) refs/heads/` and cross-reference each branch against `worktrees` (matching canonical paths via `filepath.EvalSymlinks`) to populate `checked_out_in` and `is_main_wt` (`true` only when `checked_out_in` matches the `is_main == true` worktree).
- [x] **GITOPS-BR-002**: When `ListBranches(repoPath, worktrees)` parses `%(upstream:track)`, `GITOPS` shall extract integer `ahead` and `behind` counts, and if `%(upstream:track)` is `[gone]`, set `ahead: 0`, `behind: 0`, and `gone: true`.

## Upstream & Remote Management

- [x] **GITOPS-REM-001**: When `ListRemotes(repoPath)` is called, `GITOPS` shall parse `git -C <repoPath> remote -v`, deduplicate remote entries by name, normalize SSH/HTTPS Git URLs into a browser-clickable `web_url` and `repo_slug` (`owner/repo`), and return an empty slice when no remotes are configured (`is_local_only == true`).
- [x] **GITOPS-REM-002**: When `ListRemoteIssuesAndPRs(repoPath, remoteName)` is called for a repository with at least one configured remote, `GITOPS` shall query open issues and pull requests in read-only mode via `gh issue list` and `gh pr list` (falling back to the GitHub REST API `/repos/<owner>/<repo>/issues` and `/pulls` when `gh` is unavailable) and return each item's `number`, `title`, `url`, `state`, `author`, and PR branch metadata.
- [x] **GITOPS-UP-001**: When `ListRemoteRefs(repoPath)` is called, `GITOPS` shall return all refs from `git -C <repoPath> for-each-ref --format=%(refname:short) refs/remotes/` excluding symbolic `*/HEAD` refs and bare remote names without a `/` branch separator.
- [x] **GITOPS-UP-002**: When `SetBranchUpstream(repoPath, branch, upstream)` is called with a non-empty `upstream`, `GITOPS` shall execute `git -C <repoPath> branch --set-upstream-to=<upstream> <branch>`; when called with an empty `upstream`, `GITOPS` shall execute `git -C <repoPath> branch --unset-upstream <branch>`.
- [x] **GITOPS-UP-003**: When `FetchRepo(repoPath)` is called, `GITOPS` shall execute `git -C <repoPath> fetch --all --prune` with `GIT_TERMINAL_PROMPT=0` and a `30s` `context.WithTimeout`.

## Branch & Worktree Deletion Rules

- [x] **GITOPS-DEL-001**: If direct branch deletion (`DeleteBranch`) is requested for a branch currently checked out in any worktree (`checked_out_in != ""`), or worktree removal (`DeleteWorktree`) is requested for the primary worktree (`is_main == true`), then `GITOPS` shall reject the operation with an error before invoking `git branch` or `git worktree remove`.
- [x] **GITOPS-DEL-002**: When deleting an unattached local branch (`checked_out_in == ""`), `GITOPS` shall execute `git -C <repoPath> branch -d <branch>` when `force == false` and `git -C <repoPath> branch -D <branch>` when `force == true`.
- [x] **GITOPS-DEL-003**: If `git -C <repoPath> branch -d <branch>` fails with `stderr` indicating the branch is `"not fully merged"`, then `GITOPS` shall return a structured `ErrNotFullyMerged` error containing the raw `git_error` output.
- [x] **GITOPS-DEL-004**: When removing a linked worktree (`is_main == false`), `GITOPS` shall execute `git -C <repoPath> worktree remove <wt_path>` (adding `--force` when `force_worktree == true`) before attempting any requested branch deletion; if `git worktree remove` fails without `--force` due to modified or untracked files, `GITOPS` shall return `ErrWorktreeDirty` containing `git_error`.
- [x] **GITOPS-DEL-005**: When removing a linked worktree with `delete_branch == true`, if `branch == ""` (detached `HEAD`), `GITOPS` shall skip branch deletion and return `worktree_removed: true, branch_deleted: false`; if `branch != ""` and `git branch -d` fails with `ErrNotFullyMerged` after worktree removal succeeds, `GITOPS` shall return `worktree_removed: true, branch_deleted: false, requires_force: true` along with `git_error`.
- [D] **GITOPS-WT-003**: When a linked worktree directory has been deleted outside `repotier`, `GITOPS` shall support running `git -C <repoPath> worktree prune` to clean up stale worktree administrative entries.
