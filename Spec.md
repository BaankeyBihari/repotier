### Should you use `agy` CLI or Antigravity IDE (`agy-ide`)?

Use **Antigravity IDE (`agy-ide`)** for the initial build, and reserve the **`agy` CLI** for incremental follow-ups:

* **Why `agy-ide` wins for scaffolding:** You are building a multi-package Go backend alongside an embedded single-page frontend (`ui/index.html`) that needs live browser verification (testing dropdowns, modals, and editor launch triggers against real local Git worktrees). In `agy-ide`, the agent can scaffold the project tree, run `go test`, start the daemon, and iterate on the UI while you inspect the live localhost dashboard side-by-side.
* **When to use `agy` CLI:** Once the project compiles and the daemon runs, use `agy` CLI directly inside the repository for quick surgical edits (e.g., adding a new editor binary path or tweaking a Git flag).

---

### Complete Specification Document (`SPEC.md`)

Save the specification below as `SPEC.md` in an empty project directory and instruct Antigravity to implement it end-to-end.

```markdown
# Project Specification: `gitmgr` — Local Git Worktree & Branch Manager

## 1. Overview
`gitmgr` is a single-binary Go CLI and embedded localhost web dashboard (`127.0.0.1`) for managing local Git repositories, linked worktrees, branch upstreams, and launching worktrees in local IDEs/editors.

- **Language & Runtime:** Go 1.22+ (using standard library `net/http` method+path routing and `//go:embed` for zero-build-step frontend assets).
- **Target OS:** macOS and Linux.
- **Network Scope:** Strictly `127.0.0.1` (localhost only).

---

## 2. CLI Behavior & Subcommands

### 2.1 Global Pre-Run Hook (`PersistentPreRunE`)
For every command **except** `shutdown` and `daemon`:
1. **Repository Discovery:** Execute `git rev-parse --path-format=absolute --git-common-dir` in the current working directory.
   - If the command succeeds, strip the trailing `/.git` suffix (and resolve symlinks via `filepath.EvalSymlinks`) to obtain the **canonical main repository root path**.
   - Upsert this path into the persistent repository cache (`~/.cache/gitmgr/repos.json`) with the current timestamp (`last_seen`).
   - If not inside a Git repository, do not fail—simply skip cache insertion.
2. **Daemon Auto-Start:**
   - Read active port from `~/.cache/gitmgr/daemon.port` (default `45910`, or configured port from `~/.config/gitmgr/config.json`).
   - Probe `GET http://127.0.0.1:<port>/api/health` (timeout `200ms`).
   - If unreachable, spawn `<executable> daemon --port <port>` as a detached background process (`SysProcAttr{Setsid: true}`, standard descriptors detached) and poll `/api/health` every `100ms` for up to `3s` until healthy.

### 2.2 Subcommands
- `gitmgr` (default) / `gitmgr dashboard`:
  - Runs Pre-Run Hook.
  - Opens `http://127.0.0.1:<port>` in the default system browser (`open` on macOS, `xdg-open` on Linux).
  - If invoked inside a valid Git repository, appends `?repo=<url-encoded-canonical-path>` so the dashboard pre-selects that repo.
- `gitmgr daemon [--port <port>]`:
  - Starts the HTTP server bound strictly to `127.0.0.1:<port>`.
  - Writes `~/.cache/gitmgr/daemon.pid` and `~/.cache/gitmgr/daemon.port` on startup; removes them on clean exit.
- `gitmgr shutdown`:
  - Reads port from `~/.cache/gitmgr/daemon.port`.
  - Sends `POST http://127.0.0.1:<port>/api/shutdown`.
  - Triggers graceful `http.Server.Shutdown(ctx)` with a 5-second timeout. Prints confirmation (or "Daemon is not running" if unreachable).
- `gitmgr update`:
  - Uses `github.com/creativeprojects/go-selfupdate` to check the configured GitHub repository releases for a newer version.
  - Downloads and atomically replaces the current binary.
  - If the daemon was running prior to update, sends `POST /api/shutdown` and starts the newly updated daemon binary.

---

## 3. Storage & Configuration Schemas

### 3.1 Repository Cache (`~/.cache/gitmgr/repos.json`)
Protected by an in-process `sync.Mutex` + file lock (`flock`) to prevent concurrent CLI/daemon corruption:
```json
{
  "repos": [
    {
      "path": "/Users/dev/projects/service-a",
      "last_seen": "2026-11-15T18:00:00Z"
    }
  ]
}

```

### 3.2 User Configuration (`~/.config/gitmgr/config.json`)

```json
{
  "port": 45910,
  "editor_overrides": {
    "agy": "",
    "vscode": "",
    "windsurf": "",
    "intellij": "",
    "subl": ""
  }
}

```

---

## 4. Git Domain Rules & Operations

All Git operations execute `git -C <repo_path> ...` via `os/exec` to preserve user SSH agents, credential helpers, and `.gitconfig`.

### 4.1 Repository Health Check

When listing cached repositories:

* Verify `os.Stat(path)` exists and `git -C <path> rev-parse --git-common-dir` succeeds.
* Return `"exists": true` or `"exists": false`.

### 4.2 Worktrees (`git worktree list --porcelain`)

* Parse `worktree <path>`, `HEAD <sha>`, `branch refs/heads/<name>`, and `detached` state.
* **Primary Worktree Identification:** The **first** entry in `git worktree list --porcelain` is always the primary repository working directory (`"is_main": true`). All subsequent entries are linked worktrees (`"is_main": false`).
* **Dirty & Sync Metadata:** For each worktree path that exists on disk, check:
* `dirty_count`: line count of `git -C <wt_path> status --porcelain`.
* `ahead` / `behind`: parsed from `git for-each-ref` upstream tracking info.



### 4.3 Local Branches & Git-Native Protection Model

Query via `git for-each-ref --format="%(refname:short)|%(upstream:short)|%(upstream:track)|%(HEAD)" refs/heads/`:

* **No arbitrary branch name restrictions** (e.g., `main` or `master` are NOT hardcoded as protected).
* Cross-reference every branch against active worktrees:
* `checked_out_in`: path of the worktree where this branch is currently checked out (empty if not checked out anywhere).
* `is_main_wt`: `true` **only** if `checked_out_in` matches the primary worktree (`is_main: true`).


* **Deletion Rules (Per Git Design):**
1. A branch with `is_main_wt == true` **cannot** be deleted directly (since Git forbids deleting a branch checked out in the main working directory).
2. A branch checked out in a linked worktree (`is_main_wt == false && checked_out_in != ""`) can be deleted **after** removing its linked worktree (`git worktree remove` must execute before `git branch -d/-D`).
3. Any unattached branch (`checked_out_in == ""`) can be deleted directly.
4. **Safe `-d` with `-D` Force Option:**
* Default branch deletion uses `git branch -d <branch>`.
* Users may select an explicit **"Force delete (-D)"** checkbox upfront, which executes `git branch -D <branch>`.
* If `-d` fails with `"not fully merged"`, the API returns `"requires_force": true` (HTTP `409 Conflict`) so the UI can immediately prompt the user to retry with `-D`.





### 4.4 Upstream Management

* **Set/Change Upstream:** `git -C <repo_path> branch --set-upstream-to=<remote_ref> <branch>`
* **Unset Upstream:** `git -C <repo_path> branch --unset-upstream <branch>`
* **Available Remote Refs:** `git -C <repo_path> for-each-ref --format="%(refname:short)" refs/remotes/` (exclude `*/HEAD`).

---

## 5. Editor Auto-Detection & Browser Launch

Supported editors and resolution order:

| ID | Label | CLI Binaries (`LookPath`) | Extra Probe Paths | macOS `.app` Fallback (`open -a`) |
| --- | --- | --- | --- | --- |
| `agy` | Antigravity IDE | `agy`, `agy-ide` | `/Applications/Antigravity.app/Contents/Resources/app/bin/agy` | `Antigravity` |
| `vscode` | VS Code | `code` | `/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code` | `Visual Studio Code` |
| `windsurf` | Windsurf / Devin Desktop | `windsurf`, `surf`, `devin` | `/Applications/Windsurf.app/Contents/Resources/app/bin/windsurf` | `Windsurf`, `Devin` |
| `intellij` | IntelliJ IDEA | `idea` | `~/Library/Application Support/JetBrains/Toolbox/scripts/idea`, `/Applications/IntelliJ IDEA.app/Contents/MacOS/idea`, `/Applications/IntelliJ IDEA CE.app/Contents/MacOS/idea` | `IntelliJ IDEA`, `IntelliJ IDEA CE` |
| `subl` | Sublime Text | `subl` | `/Applications/Sublime Text.app/Contents/SharedSupport/bin/subl` | `Sublime Text` |

* **Resolution Priority:**
1. Non-empty `override_path` from `~/.config/gitmgr/config.json` (verified via `os.Stat`).
2. First match in `exec.LookPath`.
3. First existing file in `ExtraPaths`.
4. On `darwin`, first existing `/Applications/<AppName>.app` (launched via `open -a "<AppName>" "<worktree_path>"`).


* **Process Lifecycle:** Spawn editors asynchronously with `cmd.Start()` and reap in a goroutine (`go func() { _ = cmd.Wait() }()`) to prevent zombie processes.

---

## 6. REST API Specification (`127.0.0.1:<port>`)

Include a `localhostGuard` middleware that rejects non-`GET`/`HEAD` requests whose `Origin` header is non-empty and not `http://127.0.0.1:<port>` or `http://localhost:<port>`.

* `GET /api/health` → `200 OK` `{"status":"ok","version":"...","pid":1234}`
* `POST /api/shutdown` → `202 Accepted` (triggers graceful shutdown)
* `GET /api/repos` → Returns all cached repos with `path`, `name`, `exists`, `last_seen`.
* `DELETE /api/repos` → Body `{"path":"..."}` (or `{"prune_missing":true}`) — removes entry from cache.
* `GET /api/repo/details?path=<encoded_path>` → Returns:
* `exists` (bool)
* `worktrees` (`path`, `head`, `branch`, `is_main`, `dirty_count`)
* `branches` (`name`, `upstream`, `track_status`, `checked_out_in`, `is_main_wt`)
* `remote_refs` (`[]string` for upstream dropdown)


* `POST /api/branches/upstream` → Body `{"repo_path":"...","branch":"...","upstream":"origin/feat"}` (empty `upstream` unsets it).
* `DELETE /api/branches` → Body `{"repo_path":"...","branch":"...","force":false}` → Returns `200 OK` or `409 Conflict` with `{"branch_deleted":false,"requires_force":true,"git_error":"..."}`.
* `DELETE /api/worktrees` → Body `{"repo_path":"...","worktree_path":"...","branch":"...","delete_branch":true,"force":false}`:
* Removes worktree first (`git worktree remove [--force] <worktree_path>`).
* If `delete_branch` is true, attempts branch deletion (`-d`, or `-D` if `force: true`). If worktree succeeds but branch deletion fails due to unmerged commits, returns `409 Conflict` with `{"worktree_removed":true,"branch_deleted":false,"requires_force":true,"git_error":"..."}`.


* `GET /api/editors` → Lists all 5 editors with `id`, `label`, `detected_path`, `override_path`, `available`.
* `PUT /api/settings/editors` → Body `{"overrides":{"agy":"/custom/path",...}}` — validates path exists and saves to `config.json`.
* `POST /api/worktrees/open` → Body `{"editor_id":"vscode","worktree_path":"..."}` — launches editor for the worktree directory.

---

## 7. Frontend Dashboard (`internal/web/ui/index.html`)

Build a clean, responsive single-page dashboard using embedded HTML/CSS/Vanilla JS (or Alpine.js via bundled local asset—no external CDN dependency required at runtime):

### 7.1 Top Navigation Bar

* **Repository Dropdown:**
* Lists **only** cached repositories where `exists == true`.
* If the URL contains `?repo=<path>` for a repo that has `exists == false`, do **not** render a broken repo view; show a dismissible warning banner (*"Repository folder is no longer present or reachable"*) and auto-select the first available valid repository.
* If no valid repositories exist in cache, render an empty state instructing the user to run `gitmgr` inside any Git repository.


* **View Switcher Tabs:** `Workspace` | `Settings`
* **Manual Refresh Button** + automatic silent refresh on `window.focus` (`visibilitychange`).

### 7.2 Workspace Tab

1. **Worktrees Section:**
* Displays each worktree's path, checked-out branch badge, `Main` badge (for `is_main: true`), dirty file indicator, and actions:
* **"Open in ▾" Dropdown:** Lists all editors from `GET /api/editors`. Available editors launch immediately via `POST /api/worktrees/open`; unavailable editors appear disabled with a shortcut link to Settings.
* **"Remove" Button** (hidden/disabled on `is_main: true`): Opens a confirmation modal with:
* Checkbox: `Also delete local branch (<branch>)`
* Sub-checkbox (enabled when above is checked): `Force delete unmerged branch (-D)`
* Checkbox: `Force remove worktree (--force)` (pre-highlighted if `dirty_count > 0`)
* If submission returns `requires_force: true` (`409`), update modal inline to show the Git warning and a **"Force Delete Branch (-D)"** retry button.






2. **Local Branches Section:**
* Table showing **Branch Name**, **Current Upstream** (with ahead/behind badge), **Checked-Out Worktree** (if any), and **Actions**:
* **Edit Upstream:** Inline dropdown/combobox populated with `remote_refs` + an `"Unset upstream"` option, saving immediately via `POST /api/branches/upstream`.
* **Delete Branch:**
* Disabled with tooltip *"Checked out in main repository directory"* when `is_main_wt == true`.
* For unattached branches, opens confirmation modal with a `Force delete (-D)` checkbox and automatic `-D` escalation if `-d` fails on unmerged commits.







### 7.3 Settings Tab

1. **Cached Repositories Table:**
* Lists **all** cached entries (both `Active` and `Unreachable` with status badges), `last_seen` timestamp, individual **Remove** button (`DELETE /api/repos`), and a header **"Prune Unreachable"** bulk button.


2. **Editor Binary Overrides:**
* Lists all 5 editors (`Antigravity IDE`, `VS Code`, `Windsurf / Devin Desktop`, `IntelliJ IDEA`, `Sublime Text`), showing their auto-detected path, an editable **Override Path** input, a **Save** button (`PUT /api/settings/editors`), and a **Test Launch** button.


3. **Daemon Info:**
* Displays current version, PID, bound address (`127.0.0.1:<port>`), and a **Shutdown Daemon** button.



---

## 8. Suggested Directory Structure

```text
gitmgr/
├── go.mod
├── main.go
├── cmd/
│   ├── root.go          # Cobra root, repo discovery, cache upsert, daemon auto-start
│   ├── daemon.go        # HTTP server lifecycle, PID/port files
│   ├── dashboard.go     # Browser launcher
│   ├── shutdown.go      # Calls POST /api/shutdown
│   └── update.go        # GitHub release self-updater + daemon restart
├── internal/
│   ├── cache/
│   │   └── cache.go     # File-locked JSON repo cache & health verification
│   ├── config/
│   │   └── config.go    # ~/.config/gitmgr/config.json loader/saver
│   ├── editors/
│   │   └── editors.go   # Auto-detection, override resolution, non-blocking launch
│   ├── gitops/
│   │   └── gitops.go    # Wrappers for worktree, branch, upstream, and rev-parse commands
│   └── web/
│       ├── server.go    # REST API handlers + localhostOriginGuard
│       └── ui/
│           └── index.html
└── internal/gitops/gitops_test.go  # Integration tests using temp git repos & worktrees

```

```

```