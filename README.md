# `repotier` — Local Git Worktree & Branch Manager

`repotier` is a single-binary Go CLI paired with an embedded localhost web dashboard (`127.0.0.1`) for managing local Git repositories, linked worktrees (`git worktree`), branch upstreams, and launching worktrees in local IDEs (`Antigravity IDE`, `VS Code`, `Windsurf / Devin`, `IntelliJ IDEA`, `Sublime Text`).

## Features

- **Zero-Friction Auto-Discovery:** Run `repotier` inside any Git working tree or linked worktree to automatically register the canonical repository root in `~/.cache/repotier/repos.json`, start the background localhost daemon if not already running, and open the browser pre-selected to that repository.
- **Worktree Lifecycle & IDE Launcher:** Inspect primary and linked worktrees, dirty file counts, launch any worktree in your preferred editor (`Open in ▾`), and safely remove linked worktrees (with optional `--force` and branch deletion).
- **Local Branch & Upstream Management:** View ahead/behind/gone tracking status, change or unset upstream refs inline, and delete branches following Git's native safety rules (`-d` by default with inline `-D` escalation on unmerged commits).
- **Single Self-Contained Binary:** Zero external runtime or CDN dependencies (`//go:embed` frontend, zero third-party Go module dependencies).
- **CalVer Self-Updater:** Run `repotier update` to fetch the latest `YYYY.MM.N` release from GitHub Releases, atomically swap the executable, and restart the daemon.

## Installation

### Quick Install (macOS & Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/BaankeyBihari/repotier/main/scripts/install.sh | sh
```

*(Set `INSTALL_DIR=/custom/bin` or `REPOTIER_VERSION=v2026.11.1` if needed.)*

### From GitHub Releases

Download the latest `repotier_<os>_<arch>.tar.gz` archive for macOS (`darwin_arm64`, `darwin_amd64`) or Linux (`linux_amd64`, `linux_arm64`) from [GitHub Releases](https://github.com/BaankeyBihari/repotier/releases) and place `repotier` on your `PATH`.

### With `go install`

```bash
go install github.com/BaankeyBihari/repotier/cmd/repotier@latest
```

### From Source (via `mise`)

```bash
mise run build     # builds ./repotier
mise run install   # installs into $GOPATH/bin or ~/go/bin
```

## Usage

```bash
repotier                      # Discover current Git repo, auto-start daemon, open dashboard
repotier dashboard            # Same as above
repotier daemon --port 45910  # Run HTTP server in foreground on 127.0.0.1:45910
repotier shutdown             # Gracefully stop the background daemon
repotier update               # Self-update from GitHub Releases and restart daemon
```

## Configuration & Storage

| File | Purpose |
|---|---|
| `~/.cache/repotier/repos.json` | File-locked (`flock`) persistent cache of discovered Git repositories. |
| `~/.cache/repotier/daemon.port` | Active localhost port of the running daemon. |
| `~/.cache/repotier/daemon.pid` | Process ID of the running daemon. |
| `~/.config/repotier/config.json` | Default daemon port (`45910`) and custom editor binary/app overrides (`agy`, `vscode`, `windsurf`, `intellij`, `subl`). |

## Architecture & Design Docs

This repository follows **Linked-Intent Development (LID)**:

- High-Level Design: [docs/high-level-design.md](docs/high-level-design.md)
- Component LLDs & EARS Specs:
  - `CLI`: [cli-design.md](docs/intent/cli/cli-design.md) · [cli-specs.md](docs/intent/cli/cli-specs.md)
  - `STORAGE`: [storage-design.md](docs/intent/storage/storage-design.md) · [storage-specs.md](docs/intent/storage/storage-specs.md)
  - `GITOPS`: [gitops-design.md](docs/intent/gitops/gitops-design.md) · [gitops-specs.md](docs/intent/gitops/gitops-specs.md)
  - `WEB`: [web-design.md](docs/intent/web/web-design.md) · [web-specs.md](docs/intent/web/web-specs.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, `mise` tasks, pre-commit hooks, and the Linked-Intent Development workflow.

## License

Released under the [MIT License](LICENSE).
