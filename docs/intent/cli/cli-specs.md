# CLI & Process Lifecycle Specs (`CLI`)

## Subcommand Dispatch & Validation

- [x] **CLI-CMD-001**: When `repotier` is invoked with no subcommand or with the `dashboard` subcommand, the CLI shall execute the pre-run hook and launch the system default browser (`open` on macOS, `xdg-open` on Linux) pointed to `http://127.0.0.1:<port>`, appending `?repo=<url_encoded_canonical_path>` when invoked inside a valid Git repository.
- [x] **CLI-CMD-002**: If `repotier` is invoked with an unknown subcommand, an unknown flag, or a `daemon --port` value outside `0..65535`, then the CLI shall print usage information to `stderr` and exit with code `2`.

## Pre-Run Hook & Daemon Auto-Start

- [x] **CLI-HOOK-001**: When the `repotier` pre-run hook runs (`dashboard` or `update` subcommands) inside a Git working tree or linked worktree, the CLI shall resolve the canonical main repository root via `GITOPS` discovery and upsert that path into `~/.cache/repotier/repos.json` before probing the daemon.
- [x] **CLI-HOOK-002**: If the `repotier` pre-run hook runs outside a Git repository, then the CLI shall skip repository cache insertion without returning an error and proceed to daemon health probing.
- [x] **CLI-HOOK-003**: When resolving the active daemon port, the CLI shall first read `~/.cache/repotier/daemon.port` and probe `GET http://127.0.0.1:<daemon_port>/api/health` (`200ms` timeout); if `daemon.port` is missing, invalid, or unreachable (stale), the CLI shall fall back to the `port` configured in `~/.config/repotier/config.json` (default `45910`).
- [x] **CLI-HOOK-004**: When the `repotier` pre-run hook finds no healthy daemon on `daemon.port` or the configured `port`, the CLI shall spawn `<executable> daemon --port <config_port>` as a detached session leader (`Setsid: true`, standard descriptors redirected to `/dev/null`) and poll `GET /api/health` every `100ms` for up to `3s` until healthy.
- [x] **CLI-HOOK-005**: If the detached daemon spawned by the pre-run hook does not respond with `200 OK` on `GET /api/health` within `3s`, then the CLI shall return a startup timeout error and exit with a non-zero status code.

## Daemon Lifecycle & Shutdown

- [x] **CLI-DAEMON-001**: When `repotier daemon [--port <port>]` starts, the daemon shall bind a TCP listener strictly to `127.0.0.1:<port>` before writing its PID to `~/.cache/repotier/daemon.pid` and the bound TCP port to `~/.cache/repotier/daemon.port`.
- [x] **CLI-DAEMON-002**: When the running `repotier daemon` receives `SIGINT`, `SIGTERM`, or an internal shutdown signal from `POST /api/shutdown`, the daemon shall execute `http.Server.Shutdown` with a `5s` timeout and remove `~/.cache/repotier/daemon.pid` and `~/.cache/repotier/daemon.port` before exiting.
- [x] **CLI-DAEMON-003**: When `repotier shutdown` is invoked and `POST http://127.0.0.1:<port>/api/shutdown` succeeds with `202 Accepted`, the CLI shall poll `GET /api/health` for up to `5s` until unreachable, print a shutdown confirmation message, and exit `0`.
- [x] **CLI-DAEMON-004**: If `repotier shutdown` cannot connect to `http://127.0.0.1:<port>/api/shutdown`, then the CLI shall remove any stale `~/.cache/repotier/daemon.pid` and `~/.cache/repotier/daemon.port` files, print `"Daemon is not running"`, and exit `0`.

## Self-Update & Build Tooling

- [x] **CLI-UPDATE-001**: When `repotier update` is invoked, the CLI shall query the GitHub Releases API (`BaankeyBihari/repotier/releases/latest`) with a `10s` timeout, compare `tag_name` against the current binary `Version` using CalVer `YYYY.MM.N` 3-integer tuple ordering (stripping any leading `v` prefix), and if `tag_name` is newer, download the matching `GOOS`/`GOARCH` asset and atomically replace the current executable via `os.Rename`.
- [x] **CLI-UPDATE-002**: When `repotier update` replaces the binary while a `repotier daemon` instance was running prior to the update, the CLI shall send `POST /api/shutdown` to stop the old daemon and auto-start the newly updated daemon binary.
- [x] **CLI-BUILD-001**: The repository shall organize the `repotier` Go package and embedded `ui/index.html` under `cmd/repotier/`, define `mise.toml` tasks (`build`, `clean`, `test`, `lint`, `install`, `hooks`) with `go`, `golangci-lint`, and `pre-commit` tools, and configure `.pre-commit-config.yaml` and `.golangci.yml` to run Go formatting and lint checks on pre-commit.
- [D] **CLI-UPDATE-003**: If the current `repotier` executable resides on a read-only mount during `repotier update`, then the CLI shall provide a fallback user-writable install path.
