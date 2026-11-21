---
parent: high-level-design
prefix: WEB
---

# HTTP Server, Editor Launcher & Web Dashboard (`WEB`)

## Context and Design Philosophy

The `WEB` segment owns the localhost HTTP server (`127.0.0.1:<port>`), cross-origin protection (`localhostGuard`), JSON REST API endpoints, IDE/editor auto-detection and non-blocking process launching, and the single-file embedded web dashboard (`ui/index.html`).

## Security & Routing (`127.0.0.1`)

- **Loopback Binding:** The HTTP listener binds strictly to `127.0.0.1:<port>`.
- **`localhostGuard` Middleware:**
  - Applied to all routes.
  - For any request whose method is not `GET` or `HEAD`, inspects the `Origin` header.
  - If `Origin` is non-empty and does not match `http://127.0.0.1:<port>` or `http://localhost:<port>`, responds immediately with `403 Forbidden` (`{"error":"forbidden origin"}`).

## REST API Contract

All non-`2xx` and non-`409` error responses return a standard JSON error envelope:

```json
{
  "error": "human-readable message",
  "git_error": "optional raw git stderr"
}
```

| Endpoint | Method | Request Body / Query | Response (`200 OK` unless noted) |
|---|---|---|---|
| `/api/health` | `GET` | — | `{"status":"ok","version":"...","pid":1234,"port":45910}` |
| `/api/shutdown` | `POST` | — | `202 Accepted` `{"status":"shutting_down"}` (triggers async `Server.Shutdown`) |
| `/api/repos` | `GET` | — | `{"repos":[{"path":"...","name":"...","exists":true,"last_seen":"...","remotes":[...]}]}` |
| `/api/repos` | `DELETE` | `{"path":"..."}` or `{"prune_missing":true}` | `{"status":"ok"}` |
| `/api/repo/details` | `GET` | `?path=<url_encoded_path>` | `{"exists":true,"is_local_only":false,"remotes":[...],"worktrees":[...],"branches":[...],"remote_refs":[...]}` |
| `/api/repo/remote-items` | `GET` | `?path=<url_encoded_path>[&remote=<name>]` | `{"is_local_only":false,"remote":"origin","issues":[...],"prs":[...]}` |
| `/api/repo/fetch` | `POST` | `{"repo_path":"..."}` | `{"status":"ok"}` (`400` on invalid input/git failure) |
| `/api/branches/upstream` | `POST` | `{"repo_path":"...","branch":"...","upstream":"..."}` | `{"status":"ok"}` (`400` on invalid input/git failure) |
| `/api/branches` | `DELETE` | `{"repo_path":"...","branch":"...","force":false}` | `{"branch_deleted":true}` or `409 Conflict` `{"branch_deleted":false,"requires_force":true,"git_error":"..."}` (`400` if checked out in any worktree) |
| `/api/worktrees` | `DELETE` | `{"repo_path":"...","worktree_path":"...","branch":"...","delete_branch":true,"force":false,"force_worktree":false}` | `{"worktree_removed":true,"branch_deleted":true}` or `409 Conflict` `{"worktree_removed":true,"branch_deleted":false,"requires_force":true,"git_error":"..."}` (`400` on dirty worktree without `force_worktree` or `is_main`) |
| `/api/editors` | `GET` | — | `{"editors":[{"id":"agy","label":"Antigravity IDE","detected_path":"...","override_path":"...","available":true},...]}` |
| `/api/settings/editors` | `PUT` | `{"overrides":{"agy":"/custom/path",...}}` | `{"status":"ok","editors":[...]}` (`400 Bad Request` if any non-empty override path fails validation) |
| `/api/settings/logging` | `GET` | — | `{"log_path":"...","logging":{"level":"info","max_size_mb":5,"max_backups":3}}` |
| `/api/settings/logging` | `PUT` | `{"level":"debug","max_size_mb":10,"max_backups":5}` | `{"status":"ok","log_path":"...","logging":{...}}` (`400 Bad Request` on invalid level/bounds; updates active daemon logger live) |
| `/api/logs` | `GET` | `?lines=100` | `{"path":"...","lines":["..."]}` (last `N` lines, max `500`, of `daemon.log`) |
| `/api/worktrees/open` | `POST` | `{"editor_id":"vscode","worktree_path":"..."}` | `{"status":"launched"}` (resolves empty or `"~"` `worktree_path` to `os.UserHomeDir()`; `400`/`404` if editor unavailable or target path missing) |

## Editor Auto-Detection & Process Launch

### Supported Editors Matrix

| ID | Label | CLI Binaries (`exec.LookPath`) | Extra Probe Paths | macOS `.app` Fallback (`open -a`) |
|---|---|---|---|---|
| `agy` | Antigravity IDE | `agy`, `agy-ide` | `/Applications/Antigravity.app/Contents/Resources/app/bin/agy` | `Antigravity` |
| `vscode` | VS Code | `code` | `/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code` | `Visual Studio Code` |
| `windsurf` | Windsurf / Devin Desktop | `windsurf`, `surf`, `devin` | `/Applications/Windsurf.app/Contents/Resources/app/bin/windsurf` | `Windsurf`, `Devin` |
| `intellij` | IntelliJ IDEA | `idea` | `~/Library/Application Support/JetBrains/Toolbox/scripts/idea`, `/Applications/IntelliJ IDEA.app/Contents/MacOS/idea`, `/Applications/IntelliJ IDEA CE.app/Contents/MacOS/idea` | `IntelliJ IDEA`, `IntelliJ IDEA CE` |
| `subl` | Sublime Text | `subl` | `/Applications/Sublime Text.app/Contents/SharedSupport/bin/subl` | `Sublime Text` |

### Resolution Priority & Launch

1. Non-empty `override_path` in `~/.config/repotier/config.json` (`~/` expanded) that passes `os.Stat`.
2. First matching binary in `exec.LookPath`.
3. First existing path in `Extra Probe Paths` (`~/` expanded to user home directory).
4. On `darwin`, first existing `/Applications/<AppName>.app` directory.

- **Launch Execution:** If the resolved target on `darwin` is a directory ending in `.app` (from step 1, 3, or 4), launch via `open -a "<resolved_path_or_app>" "<worktree_path>"`; otherwise execute directly as `<resolved_path> "<worktree_path>"`.
- **Non-Blocking Reap:** Editor processes are started with `cmd.Start()` and reaped asynchronously in a background goroutine (`go func() { _ = cmd.Wait() }()`) so the HTTP handler returns immediately and no zombie processes accumulate.

## Frontend Dashboard Layout (`ui/index.html`)

```text
┌────────────────────────────────────────────────────────────────────────────┐
│ repotier   [Repo: service-a ▾]          [Workspace] [Settings]   [↻ Refresh]│
├────────────────────────────────────────────────────────────────────────────┤
│ ⚠ (Optional dismissible banner if ?repo=<path> is missing/unreachable)     │
├────────────────────────────────────────────────────────────────────────────┤
│ WORKTREES                                                                  │
│ ┌────────────────────────────────────────────────────────────────────────┐ │
│ │ /Users/dev/service-a   [main] [Main] [Clean]           [Open in ▾]     │ │
│ │ /Users/dev/service-a-wt [feat/login] [2 dirty]  [Open in ▾] [Remove]   │ │
│ └────────────────────────────────────────────────────────────────────────┘ │
│ LOCAL BRANCHES                                                             │
│ ┌──────────────────┬────────────────────────┬──────────────────┬─────────┐ │
│ │ Branch           │ Upstream               │ Checked Out In   │ Actions │ │
│ ├──────────────────┼────────────────────────┼──────────────────┼─────────┤ │
│ │ main             │ origin/main [0↑ 0↓] ▾  │ /Users/... (Main)│ [🔒 Del]│ │
│ │ feat/login       │ origin/feat [1↑ 0↓] ▾  │ /Users/...-wt    │ [Delete]│ │
│ │ chore/old        │ (none)              ▾  │ —                │ [Delete]│ │
│ └──────────────────┴────────────────────────┴──────────────────┴─────────┘ │
└────────────────────────────────────────────────────────────────────────────┘
```

### UI Interaction Rules

- **Top Navigation Bar:**
  - Repository selector lists only cached repositories where `exists == true`.
  - Navigation tabs: `Workspace` | `Repositories` | `Settings`.
  - If URL query `?repo=<path>` points to a non-existent or uncached path with `exists == false`, displays a dismissible warning banner (*"Repository folder is no longer present or reachable"*) and selects the first valid cached repository.
  - If zero valid repositories exist, renders an empty-state guide instructing the user to run `repotier` inside a Git repository.
  - Silent data refresh triggers automatically on `window.focus` and `document.visibilitychange` (when `visibilityState === 'visible'`).
  - After deleting a worktree or branch, re-fetches `/api/repo/details` in place. After removing or pruning the currently selected repository in Settings, re-fetches `/api/repos`, switches selection to the first remaining valid repo (or empty-state view if none remain), and updates the URL query parameter via `history.replaceState`.
- **Workspace Tab Remotes Header & Local-Only Banner:**
  - When the active repository has configured Git remotes (`is_local_only == false`), renders a **Remotes** bar at the top of the `Workspace` tab showing a badge/link for each remote (`name` + clickable `web_url` opening in a new tab).
  - When the active repository has zero configured Git remotes (`is_local_only == true`), renders a **Local-Only Repository** banner (*"This repository is local-only (no Git remotes configured)"*) at the top of both the `Workspace` and `Repositories` tabs.
- **Repositories Tab (`Workspaces`, `Issues`, `PRs`):**
  - While on the `Repositories` tab, `#repoSelect` presents a global dropdown of all unique remote repositories discovered across all active (`exists == true`) cached workspaces, deduplicated by canonical remote identity (`web_url` / `repo_slug`, or raw URL fallback) and labeled by canonical repository slug/URL without any per-clone remote-alias prefix (e.g. `origin`).
  - Navigating from `Workspace` → `Repositories` records the originating workspace (`originatingWorkspace`) and defaults `#repoSelect` to: (1) the repository matching `origin` on the originating workspace if present, (2) else its first configured remote's repository, (3) else a disabled, initially selected `local-repo` anchor option alongside all available remote repositories (displaying the local-only banner, the originating workspace's worktrees, and hiding `Issues`/`PRs`).
  - Selecting any remote repository in `#repoSelect` (including switching off the disabled `local-repo` anchor) takes over the remote flow: hides the local-only banner, loads all matching local clones and their worktrees into the **Workspaces** table, and fetches read-only **Issues** and **Pull Requests**.
  - **Workspaces Group:** Lists all worktrees across all local clones associated with the selected repository (`Workspace Path`, `Remote` — showing the remote name(s) that clone uses for the repository or `—` when local-only — `Branch / HEAD`, `Role`, `Status`, and **View in Workspace**). Clicking **View in Workspace** jumps to that specific clone workspace and highlights the clicked worktree.
  - **Returning to Workspace (`Repositories` → `Workspace`):** If the selected repository was not changed (including remaining on the disabled `local-repo` anchor), switches back to the exact originating workspace; if the selected repository was changed, switches to the first workspace in the list of clones for the selected repository.
  - **Issues Group (Read-Only):** Hidden when the disabled `local-repo` anchor is active; otherwise lists open issues from `/api/repo/remote-items` (`#number`, `title`, `state`, `author`) with an external link (`target="_blank"`) to open the issue page.
  - **PRs Group (Read-Only):** Hidden when the disabled `local-repo` anchor is active; otherwise lists open pull requests from `/api/repo/remote-items` (`#number`, `title`, `head → base`, `draft`/`open` badge, `author`) with an external link (`target="_blank"`) to open the PR page.
- **Worktree Removal Modal:**
  - Hidden/disabled on primary worktree (`is_main: true`).
  - Checkboxes:
    1. `Also delete local branch (<branch>)` (shown when worktree is on a named branch).
    2. `Force delete unmerged branch (-D)` (enabled when checkbox 1 is checked).
    3. `Force remove worktree (--force)` (pre-highlighted/warned when `dirty_count > 0`).
  - If `DELETE /api/worktrees` returns `409 Conflict` with `requires_force: true` (meaning worktree was removed, but branch `-d` failed due to unmerged commits), the modal stays open, displays `git_error`, and switches its primary action to **"Force Delete Branch (-D)"** (`DELETE /api/branches` with `force: true`).
- **Branch Deletion Modal:**
  - Delete button is disabled with tooltip *"Checked out in main repository directory"* when `is_main_wt == true`.
  - For branches checked out in a linked worktree (`checked_out_in != ""` and `is_main_wt == false`), clicking Delete opens the **Worktree Removal Modal** for `checked_out_in` with `Also delete local branch (<branch>)` pre-checked (`DELETE /api/worktrees`).
  - For unattached branches (`checked_out_in == ""`), opens modal with `Force delete (-D)` checkbox and inline `409 Conflict` retry escalation (`DELETE /api/branches`).
- **Settings Tab:**
  - **Cached Repositories Table:** Shows all cached entries (`Active` vs. `Unreachable` badges), `last_seen`, per-row **Remove**, and bulk **Prune Unreachable** button.
  - **Editor Overrides:** Shows all 5 editors, `detected_path`, editable `override_path` input, **Save** button (`PUT /api/settings/editors`), and **Test Launch** button (opens current selected repo or user home directory).
  - **Daemon Info:** Displays version, PID, `127.0.0.1:<port>`, and **Shutdown Daemon** button (`POST /api/shutdown`).
  - **Daemon Logging:** Displays `log_path` (`~/.cache/repotier/daemon.log`), **Log Level** select (`debug`, `info`, `warn`, `error`), **Max Size (MB)**, **Max Backups**, **Save** button (`PUT /api/settings/logging`), and **View Last 100 Lines** button opening a log viewer modal (`GET /api/logs?lines=100`).
- **Toast Notifications & Activity Hub:**
  - Interactive action outcomes (success and failure) render as stacked, auto-dismissing toast banners (`4s` timeout, max `4` visible) in `#toastContainer`, leaving the top `#warningBanner` reserved for persistent unreachable-repository warnings.
  - Every toast event is recorded in an in-memory `activityLog` (capped at `100` entries since page launch) viewable via the top-nav **Activity bell icon** button (showing a count badge only when count > `0`) with a **Clear** action.
- **Color Palettes & Theme Switcher:**
  - Top navigation includes a `#themeSelect` dropdown offering 7 canonical palettes via CSS `[data-theme]` variables, persisted in `localStorage` (`repotier.theme`): **Midnight Slate** (default dark), **Rosé Pine** (dark rose, `#191724` / `#ebbcba`), **Rosé Pine Dawn** (light rose, `#faf4ed` / `#d7827e`), **Solarized Dark** (`#002b36` / `#268bd2`), **Solarized Light** (light gold, `#fdf6e3` / `#b58900`), **Sky Light** (light sky blue, `#f0f9ff` / `#0284c7`), and **Zinc Light** (light grey, `#f4f4f5` / `#3f3f46`).

## Decisions & Alternatives

| Decision | Chosen | Alternatives Considered | Rationale |
|---|---|---|---|
| **Frontend Packaging** | Single `ui/index.html` embedded via `//go:embed` with Vanilla JS | Separate JS/CSS build step or external CDN scripts | Zero build tooling, instant startup, works completely offline. |
| **Separate `force_worktree` vs. `force` Flags on `DELETE /api/worktrees`** | Accept both `force_worktree` (for `git worktree remove --force`) and `force` (for `git branch -D`) | Single overloaded `force` boolean | The UI modal has independent checkboxes for forcing a dirty worktree removal (`--force`) and forcing an unmerged branch deletion (`-D`). |
| **Standard Error Envelope** | `{"error":"...","git_error":"..."}` across all non-2xx/non-409 responses | Plain text `http.Error` strings | Gives the frontend consistent structured error messages and optional raw `git` stderr for modals and banners. |
| **Global Canonical Repository Selector** | Aggregate unique remote repositories across all active cached workspaces with canonical labels, plus a disabled `local-repo` arrival anchor when coming from a local-only workspace | Lock `#repoSelect` to remotes of the single active workspace | Enables seamless cross-clone repository browsing while preserving local-only navigation state and smart return-to-workspace resolution. |

## Open Questions & Future Decisions

### Resolved

1. ✅ **Origin Guard Scope:** Rejects non-`GET`/`HEAD` requests when `Origin` is present and not `http://127.0.0.1:<port>` or `http://localhost:<port>`.
2. ✅ **Standard JSON Error Envelope:** `{"error":"...","git_error":"..."}` on `400`/`403`/`404`/`500`.
3. ✅ **macOS `.app` Override Launch:** Any resolved path ending in `.app` on `darwin` launches via `open -a`.
4. ✅ **In-Session Active Repo Removal:** Re-fetch `/api/repos`, fall back to first valid repo or empty state, and update URL via `history.replaceState`.

### Deferred

1. None.

## References

- Parent HLD: [high-level-design.md](../../high-level-design.md)
- EARS Specs: [web-specs.md](web-specs.md)
