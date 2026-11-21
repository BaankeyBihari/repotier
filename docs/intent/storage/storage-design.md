---
parent: high-level-design
prefix: STORAGE
---

# Repository Cache & User Configuration (`STORAGE`)

## Context and Design Philosophy

The `STORAGE` segment manages all persistent local state on disk: the discovered-repository cache (`~/.cache/repotier/repos.json`) and user settings (`~/.config/repotier/config.json`). Because both the short-lived CLI process and the long-running HTTP daemon read and write `repos.json`, `STORAGE` enforces both in-process mutex locking and cross-process advisory file locking (`syscall.Flock`).

## File Locations & Schemas

### Repository Cache (`~/.cache/repotier/repos.json`)

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

- **Upsert (`UpsertRepo(path string)`):** Normalizes `path` via `filepath.Clean`, updates `last_seen` (RFC3339 UTC) if `path` exists in `repos`, or appends a new entry if absent.
- **List (`ListRepos()`):** Acquires the cache lock only while reading `repos.json` into memory (returning an empty slice if file does not exist), releases the lock, enriches each entry with `name: filepath.Base(path)`, `exists: bool` (checked via `GITOPS` repository health verification outside the file lock), and (when `exists == true`) its configured Git `remotes` via `GITOPS.ListRemotes` (computed transiently and omitted from `repos.json`), and sorts entries by `last_seen` descending (with `path` ascending as a deterministic tie-breaker).
- **Remove (`RemoveRepo(path string)`):** Removes the matching `path` entry from `repos.json`.
- **Prune Missing (`PruneMissingRepos()`):** Evaluates `GITOPS` health of cached paths outside the file lock, then acquires the lock to remove all entries where `exists == false`.

### User Configuration (`~/.config/repotier/config.json`)

```json
{
  "port": 45910,
  "editor_overrides": {
    "agy": "",
    "vscode": "",
    "windsurf": "",
    "intellij": "",
    "subl": ""
  },
  "logging": {
    "level": "info",
    "max_size_mb": 5,
    "max_backups": 3
  }
}
```

- **Defaults & Partial/Corrupt Recovery:**
  - If `~/.config/repotier/config.json` does not exist, `LoadConfig()` returns default `port: 45910`, empty string overrides for the 5 supported editor IDs (`agy`, `vscode`, `windsurf`, `intellij`, `subl`), and default `logging` (`level: "info"`, `max_size_mb: 5`, `max_backups: 3`).
  - Partial JSON is merged onto the default struct: if `port <= 0` or `port > 65535`, it defaults to `45910`; missing keys in `editor_overrides` default to `""`; invalid or omitted `logging` fields fall back to their defaults.
  - If either `repos.json` or `config.json` contains malformed/corrupted JSON that fails `json.Unmarshal`, `STORAGE` renames the corrupted file to `<filename>.corrupt` and falls back to the empty/default state so the tool remains operable.
- **Validation on Save (`SaveLoggingConfig(cfg LoggingConfig)`):**
  - `level` must be one of `"debug"`, `"info"`, `"warn"`, `"error"`.
  - `max_size_mb` must be in `1..500`; `max_backups` must be in `0..20`.
  - Invalid values fail validation with an error before mutating `config.json`.
- **Validation on Save (`SaveEditorOverrides(overrides map[string]string)`):**
  - Unknown editor IDs are rejected with a validation error.
  - For non-empty override paths, a leading `~/` is expanded via `os.UserHomeDir()` before checking `os.Stat(expandedPath)`.
  - Path must exist and be either a regular file or (on `darwin`) a directory ending in `.app`; otherwise validation fails before mutating `config.json`.
  - Empty string `""` clears the override for that editor ID.

## Concurrency & Atomic Persistence

1. **In-Process + OS File Lock:**
   - Every read-modify-write operation on `repos.json` acquires a package-level `sync.Mutex`, opens `~/.cache/repotier/repos.lock` (`O_CREATE|O_RDWR`, `0600`), and calls `syscall.Flock(fd, syscall.LOCK_EX)`.
2. **Atomic Write:**
   - Marshal JSON with 2-space indentation, write to a temporary file in the target directory (`repos.json.tmp` / `config.json.tmp`), `Sync()`, and `os.Rename` over the destination file before releasing `Flock` and `sync.Mutex`.

## Decisions & Alternatives

| Decision | Chosen | Alternatives Considered | Rationale |
|---|---|---|---|
| **Lockfile Separation (`repos.lock`)** | Acquire `syscall.Flock` on a dedicated companion `repos.lock` file while atomically renaming `repos.json.tmp` → `repos.json` | Flock directly on `repos.json` and truncate in place | Locking `repos.json` directly conflicts with atomic `os.Rename` replacement (which swaps inodes) and risks partial writes on crash if truncated in-place. |
| **Missing & Corrupt File Handling** | Treat `os.ErrNotExist` as valid empty/default state; back up corrupt JSON to `*.corrupt` and reset to default | Fail startup on corrupt cache/config JSON | A truncated or hand-edited config/cache file should not brick CLI or daemon startup. |
| **Repository List Ordering** | Sort by `last_seen` descending (`path` ascending tie-breaker) | Preserve raw insertion order | Ensures the most recently visited repository is always at the top of the selector. |

## Open Questions & Future Decisions

### Resolved

1. ✅ **Editor ID Set:** Fixed to `agy`, `vscode`, `windsurf`, `intellij`, `subl`.
2. ✅ **Tilde Expansion & `.app` Bundles in Overrides:** Expand `~/` before `os.Stat` and accept regular files or macOS `.app` directories.
3. ✅ **Corrupt JSON Recovery:** Back up to `*.corrupt` and reset to defaults.
4. ✅ **Deterministic Ordering:** `ListRepos()` sorts by `last_seen` descending, then `path` ascending.

### Deferred

1. Custom XDG base directory environment variable overrides (`XDG_CACHE_HOME`, `XDG_CONFIG_HOME`) vs. `~/.cache` and `~/.config` under `os.UserHomeDir()`.

## References

- Parent HLD: [high-level-design.md](../../high-level-design.md)
- EARS Specs: [storage-specs.md](storage-specs.md)
