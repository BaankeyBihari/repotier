## 2026-11-15 - HLD Architecture, Segment Partitioning, and Project Tenets
WHY: Keep codebase minimal and dependency-light (stdlib flag + flat package layout instead of Cobra and 6 nested internal packages) while preserving clean 4-segment intent boundaries (CLI, STORAGE, GITOPS, WEB) and strict Git-CLI-as-source-of-truth tenets.
ALTERNATIVES: Option A (6-package layout with spf13/cobra per Spec.md §8); Option C (ephemeral foreground server without background daemon); 6-LLD or 2-LLD segment splits.
DEFERRED: Detailed component error shapes and endpoint edge cases (deferred to Phase 2 LLDs).
## 2026-11-15 - Canonical Tool Name is repotier
WHY: Project and binary name is `repotier` (matching repo `BaankeyBihari/repotier`), replacing `gitmgr` placeholder from `Spec.md` across CLI binary, cache (`~/.cache/repotier`), and config (`~/.config/repotier`).
ALTERNATIVES: Keeping `gitmgr` from `Spec.md`.
DEFERRED: None.
## 2026-11-16 - Phase 2 LLD Edge-Case Triage & Zero-Dependency Self-Update
WHY: Folding all 12 internal edge-case probe defaults into the 4 LLDs eliminates ambiguity before writing EARS specs; using stdlib `net/http` + `archive/tar` + `compress/gzip` + `os.Rename` for `repotier update` against GitHub Releases keeps `go.mod` at zero external dependencies.
ALTERNATIVES: `github.com/creativeprojects/go-selfupdate` (~30 transitive deps); deferring edge cases.
DEFERRED: Cross-filesystem read-only mount fallback on `update`; XDG env overrides (`XDG_CACHE_HOME`/`XDG_CONFIG_HOME`); automatic `git worktree prune`.
## 2026-11-16 - Phase 4 Intent-Narrowing Cross-Segment Decisions
WHY: (1) Prevent silent worktree directory deletion from `DELETE /api/branches` by routing linked-worktree branch deletion in UI through the Worktree Removal Modal (`DELETE /api/worktrees`) with `Also delete local branch` pre-checked; (2) prevent stale `daemon.port` from overriding `config.json` port on auto-start; (3) allow Settings 'Test Launch' to work with zero cached repos by falling back to `os.UserHomeDir()` when `worktree_path` is empty/`~`; (4) avoid holding `repos.lock` across `git` health-check subprocesses.
ALTERNATIVES: Implicit worktree removal inside `DELETE /api/branches`; reusing stale `daemon.port` on spawn; disabling Test Launch when cache is empty; holding `Flock` during `git` subprocesses.
DEFERRED: None.
## 2026-11-16 - CalVer (YYYY.MM.N) & mise.toml Toolchain/Task Config
WHY: CalVer `YYYY.MM.N` (git tag `vYYYY.MM.N`, parsed as `[3]int` tuple `[year, month, release_no]`) fits a continuous-delivery CLI/daemon better than arbitrary SemVer major/minor bumps and avoids string-sort bugs (`2026.11.10 > 2026.11.2`). `mise.toml` pins `go = "1.22"` and standardizes `build`, `test`, and `install` tasks with `-ldflags "-X main.Version=..."`.
ALTERNATIVES: Date-based `YYYY.MM.DD`; SemVer `vX.Y.Z`; tools-only `mise.toml`.
DEFERRED: Automated GitHub Actions release workflow.
## 2026-11-16 - cmd/repotier Layout, mise Tasks (build/clean/lint), and Pre-Commit Hooks
WHY: Moving `package main` files and `ui/index.html` into `cmd/repotier/` keeps the repository root clean while ensuring `go build ./cmd/repotier` and `go install .../cmd/repotier@latest` name the binary `repotier` (whereas `src/` would name it `src`). Pinning `golangci-lint` and `pre-commit` in `mise.toml` with `.pre-commit-config.yaml` and `clean`/`build`/`lint` tasks standardizes local quality gates.
ALTERNATIVES: `src/` directory layout; multi-package `internal/*` layout; custom shell `.git/hooks/pre-commit` without `pre-commit`.
DEFERRED: None.
