# Repository Cache & User Configuration Specs (`STORAGE`)

## Repository Cache (`~/.cache/repotier/repos.json`)

- [x] **STORAGE-CACHE-001**: When `UpsertRepo(path)` is called for `~/.cache/repotier/repos.json`, the storage layer shall normalize `path` with `filepath.Clean`, update `last_seen` to the current UTC RFC3339 timestamp if `path` is already present, or append a new entry if absent.
- [x] **STORAGE-CACHE-002**: When `ListRepos()` is called, the storage layer shall read `~/.cache/repotier/repos.json` (returning an empty slice if the file does not exist), release the file lock before invoking `GITOPS` repository health checks to populate `name` (`filepath.Base(path)`) and `exists`, and return the entries sorted by `last_seen` descending with `path` ascending as a tie-breaker.
- [x] **STORAGE-CACHE-003**: When `RemoveRepo(path)` is called, the storage layer shall delete the matching `path` entry from `~/.cache/repotier/repos.json`.
- [x] **STORAGE-CACHE-004**: When `PruneMissingRepos()` is called, the storage layer shall evaluate `GITOPS` repository health outside the file lock and then acquire the lock to remove all entries from `~/.cache/repotier/repos.json` where `exists == false`.

## User Configuration (`~/.config/repotier/config.json`)

- [x] **STORAGE-CFG-001**: When `LoadConfig()` reads `~/.config/repotier/config.json` and the file does not exist or omits fields, the storage layer shall return default `port: 45910` (also applied whenever stored `port <= 0` or `port > 65535`) and an `editor_overrides` map initialized with `""` for `agy`, `vscode`, `windsurf`, `intellij`, and `subl`.
- [x] **STORAGE-CFG-002**: When `SaveEditorOverrides(overrides)` is called, the storage layer shall reject unknown editor IDs (outside `agy`, `vscode`, `windsurf`, `intellij`, `subl`), expand any leading `~/` on non-empty paths via `os.UserHomeDir()`, verify via `os.Stat` that each non-empty path exists as a regular file or (on `darwin`) a `.app` directory bundle, and persist the validated overrides to `~/.config/repotier/config.json`.

## Concurrency, Atomic Persistence & Corrupt Recovery

- [x] **STORAGE-LOCK-001**: When mutating `~/.cache/repotier/repos.json` or `~/.config/repotier/config.json`, the storage layer shall acquire both an in-process `sync.Mutex` and an exclusive OS file lock (`syscall.Flock` with `LOCK_EX` on a companion `.lock` file), write formatted JSON to a temporary `.tmp` file in the same directory, `Sync()`, and atomically replace the target file via `os.Rename` before releasing the locks.
- [x] **STORAGE-LOCK-002**: If `~/.cache/repotier/repos.json` or `~/.config/repotier/config.json` contains malformed JSON that fails unmarshaling, then the storage layer shall rename the corrupted file to `<filename>.corrupt` and return the empty cache or default configuration state.
- [D] **STORAGE-CFG-003**: Where `XDG_CACHE_HOME` or `XDG_CONFIG_HOME` environment variables are set, the storage layer shall resolve cache and config paths relative to those directories instead of `~/.cache` and `~/.config`.
