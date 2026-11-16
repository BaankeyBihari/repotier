---
parent: high-level-design
prefix: CLI
---

# CLI & Process Lifecycle (`CLI`)

## Context and Design Philosophy

The `CLI` segment owns command-line invocation (`repotier`), pre-run repository discovery, background daemon auto-start and health polling, PID/port file management, system browser launch, and binary self-update. It uses Go's standard library `os.Args`, `flag.NewFlagSet`, and `net/http` rather than external CLI or self-update frameworks, keeping the binary completely free of third-party Go dependencies.

## Subcommand Dispatch & Validation

`repotier` accepts an optional positional subcommand (`dashboard`, `daemon`, `shutdown`, `update`), defaulting to `dashboard` when invoked with no arguments. Unknown subcommands or unknown flags print usage to `stderr` and exit with code `2`.

| Invocation | Runs Pre-Run Hook? | Behavior |
|---|---|---|
| `repotier` / `repotier dashboard` | Yes | Discovers current Git repo, ensures daemon is running, opens system browser to `http://127.0.0.1:<port>[?repo=<encoded_path>]`. |
| `repotier daemon [--port <port>]` | No | Validates `--port` (`1..65535`, or `0` for OS-assigned ephemeral port), starts HTTP server bound to `127.0.0.1:<port>`, manages `daemon.pid` and `daemon.port`, blocks until signal or `/api/shutdown`. |
| `repotier shutdown` | No | Reads `~/.cache/repotier/daemon.port` (falling back to config/default port `45910` if missing), sends `POST http://127.0.0.1:<port>/api/shutdown`, cleans up stale PID/port files if unreachable, and exits `0`. |
| `repotier update` | Yes | Queries GitHub Releases API (`BaankeyBihari/repotier`) via stdlib `net/http`, downloads and atomically replaces executable if newer, and restarts daemon if previously running. |

### Pre-Run Hook Execution Order

1. **Repository Discovery:**
   - Execute `git rev-parse --path-format=absolute --git-common-dir` in the current working directory.
   - If exit code is `0`, trim trailing newline, strip trailing `/.git` segment via `filepath.Dir(commonDir)` when `filepath.Base(commonDir) == ".git"` (or retain `commonDir` directly when `git rev-parse --is-bare-repository` is `true`), resolve symlinks via `filepath.EvalSymlinks`, and upsert the canonical repository root path into `~/.cache/repotier/repos.json` with `last_seen: time.Now().UTC()`.
   - If not inside a Git repository (non-zero exit), skip cache insertion without error.
2. **Active Port Resolution & Daemon Auto-Start:**
   - Read `~/.cache/repotier/daemon.port`. If present and valid, probe `GET http://127.0.0.1:<daemon_port>/api/health` (`200ms` timeout). If `200 OK`, use `<daemon_port>`.
   - If `daemon.port` is missing, invalid, or its health probe fails (stale file from a dead daemon), resolve target port from `~/.config/repotier/config.json` (default `45910`) and probe `GET http://127.0.0.1:<config_port>/api/health` (`200ms` timeout).
   - If unreachable or non-`200`:
     - Resolve current binary path via `os.Executable()`.
     - Spawn `<executable> daemon --port <config_port>` with `SysProcAttr: &syscall.SysProcAttr{Setsid: true}` and `Stdin/Stdout/Stderr` bound to `/dev/null` (`os.DevNull`).
     - Poll `GET http://127.0.0.1:<config_port>/api/health` every `100ms` for up to `3s`. Return an error if the daemon does not become healthy within `3s`.

## Daemon Lifecycle & Runtime Files

When `repotier daemon [--port <port>]` starts:
1. Validate `--port` is in `0..65535` (reject `< 0` or `> 65535` with exit code `2`).
2. Ensure `~/.cache/repotier/` exists (`0755`).
3. Bind TCP listener strictly to `127.0.0.1:<port>` before writing state files so port conflicts fail immediately without clobbering an active daemon's files. Read the actual bound port via `listener.Addr().(*net.TCPAddr).Port`.
4. Write current PID to `~/.cache/repotier/daemon.pid` and actual bound port to `~/.cache/repotier/daemon.port`.
5. Listen for `SIGINT`, `SIGTERM`, or internal shutdown channel triggered by `POST /api/shutdown`.
6. On shutdown trigger, invoke `http.Server.Shutdown(ctx)` with a `5s` timeout, then remove `daemon.pid` and `daemon.port`.

## Shutdown Command (`repotier shutdown`)

1. Read target port from `~/.cache/repotier/daemon.port`; if missing or invalid, fall back to `~/.config/repotier/config.json` (`port`, default `45910`).
2. Send `POST http://127.0.0.1:<port>/api/shutdown` (`2s` timeout).
3. If `202 Accepted`, poll until health check fails (up to `5s`), print confirmation, and exit `0`.
4. If connection is refused or unreachable, remove any stale `daemon.pid` and `daemon.port` files, print `"Daemon is not running"`, and exit `0`.

## Browser Launch

- On `darwin`: `exec.Command("open", targetURL).Start()`
- On `linux`: `exec.Command("xdg-open", targetURL).Start()`
- Query parameter: when repository discovery found a valid canonical path, append `?repo=` + `url.QueryEscape(canonicalPath)`.

## Self-Update, CalVer Versioning & Developer Tooling (`mise.toml` / `pre-commit`)

`repotier` uses **CalVer (`YYYY.MM.N`)** versioning (e.g., `2026.11.1`, or Git tag `v2026.11.1`), injected at build time via `-ldflags "-X main.Version=..."` (defaulting to `2026.11.0` for local dev builds). All Go source files and the embedded `ui/index.html` live in `cmd/repotier/`.

### Build, Clean, Lint & Pre-Commit Tooling
- **`mise.toml` Tools:** `go = "1.22"`, `golangci-lint = "latest"`, `pre-commit = "latest"`.
- **`mise.toml` Tasks:**
  - `build`: `go build -ldflags "-X main.Version=${VERSION}" -o repotier ./cmd/repotier`
  - `clean`: `rm -f repotier coverage.out && go clean -testcache`
  - `test`: `go test -timeout 15s ./cmd/repotier/...`
  - `lint`: `golangci-lint run ./cmd/repotier/...`
  - `install`: `go install -ldflags "-X main.Version=${VERSION}" ./cmd/repotier`
  - `hooks`: `pre-commit install`
- **Pre-Commit Hook (`.pre-commit-config.yaml` & `.golangci.yml`):** Executes `gofmt`, `go vet`, and `golangci-lint run ./cmd/repotier/...` before commits.

### Self-Update Flow (`repotier update`)
1. Probe whether daemon is currently healthy on active port (`wasRunning`).
2. Query `GET https://api.github.com/repos/BaankeyBihari/repotier/releases/latest` (`10s` timeout) and compare `tag_name` against build-time `Version` by stripping any leading `v` prefix and comparing the parsed 3-integer tuple `(YYYY, MM, N)` so `2026.11.10 > 2026.11.2`.
3. If `tag_name` is not newer than `Version`, print confirmation and exit `0`.
4. Locate matching asset for `runtime.GOOS` and `runtime.GOARCH` (supporting `.tar.gz` archive or raw binary asset), stream and extract to a temporary file in `filepath.Dir(execPath)`, set mode `0755`, and atomically replace via `os.Rename(tmpPath, execPath)`.
5. If `wasRunning` is true, send `POST /api/shutdown`, wait until `/api/health` fails (up to `5s`), and spawn the updated binary via the auto-start flow.

## Decisions & Alternatives

| Decision | Chosen | Alternatives Considered | Rationale |
|---|---|---|---|
| **CLI Flag & Subcommand Parsing** | Stdlib `os.Args` switch + `flag.NewFlagSet` for `daemon --port` | `github.com/spf13/cobra` (`Spec.md` §8) | Only 4 subcommands and 1 flag exist; stdlib `flag` avoids external CLI dependencies and boilerplate files. |
| **Listener Bind Before PID/Port Write** | Bind `net.Listen("tcp", "127.0.0.1:<port>")` first, then write `daemon.pid` / `daemon.port` | Write PID/port files before binding socket | Prevents a duplicate `repotier daemon` invocation that fails with `EADDRINUSE` from overwriting or deleting the running daemon's PID and port files. |
| **Self-Update Implementation** | Stdlib `net/http` GitHub Releases API + `archive/tar` + `compress/gzip` + atomic `os.Rename` | `github.com/creativeprojects/go-selfupdate` | Eliminates ~30 transitive third-party dependencies while satisfying GitHub release check, atomic binary swap, and daemon restart in ~50 lines of standard library code. |
| **Versioning Scheme & Toolchain (`mise.toml`)** | CalVer `YYYY.MM.N` (3-int tuple comparison, optional `v` prefix stripped) + `mise.toml` (`go = "1.22"`, `build`/`test`/`install` tasks) | SemVer `vX.Y.Z` or lexicographical string comparison | Fits a continuous-release CLI/daemon without arbitrary major/minor bumps; tuple comparison prevents `2026.11.10 < 2026.11.2` lexical bugs in 15 lines of stdlib code. |

## Open Questions & Future Decisions

### Resolved
1. ✅ **Canonical Binary & Directory Name:** Use `repotier` (`~/.cache/repotier/`, `~/.config/repotier/`).
2. ✅ **Shutdown When Unreachable:** Clean up stale `daemon.pid`/`daemon.port`, print `"Daemon is not running"`, and exit `0`.
3. ✅ **Bare & Linked Worktree Discovery:** Strip trailing `/.git` when `filepath.Base(commonDir) == ".git"`, or retain `commonDir` when `--is-bare-repository` is `true`.
4. ✅ **Port Validation:** Allow `0..65535` on `daemon --port`; reject invalid ports or unknown flags/subcommands with exit code `2`.

### Deferred
1. Cross-filesystem fallback if `os.Executable()` resides on a read-only mount during `repotier update`.

## References

- Parent HLD: [high-level-design.md](../../high-level-design.md)
- Specification: [Spec.md](../../../Spec.md)
