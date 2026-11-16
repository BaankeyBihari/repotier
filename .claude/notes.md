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
## 2026-11-16 - Auto-CalVer GitHub Release Workflow & Self-Update Contract
WHY: Auto-computing `vYYYY.MM.N` on `main` pushes in `.github/workflows/release.yml` and packaging `repotier_${GOOS}_${GOARCH}.tar.gz` assets via stdlib cross-compile + `gh release create` ensures every merged change produces a release that `repotier update` can immediately discover and install without manual tag choreography or GoReleaser config bloat.
ALTERNATIVES: Manual tag-push trigger; GoReleaser action.
DEFERRED: Code signing / macOS notarization.
## 2026-11-16 - Native Go Cross-Compile + checksums.txt over GoReleaser
WHY: GoReleaser strictly enforces SemVer 2.0 and rejects zero-padded CalVer months (`01`..`09`, e.g., `v2027.01.1`) as invalid semantic versions. Native `CGO_ENABLED=0 go build` + `shasum -a 256 > checksums.txt` + `gh release create` supports true `YYYY.MM.N` CalVer with zero extra config files.
ALTERNATIVES: GoReleaser with unpadded `YYYY.M.N`.
DEFERRED: Homebrew tap formula generation.
## 2026-11-16 - Local Builds Use Static 'dev' Version, Releases Use CalVer YYYY.MM.N
WHY: Setting `var Version = "dev"` in `main.go` and `VERSION = "dev"` in `mise.toml` clearly distinguishes local source builds from official GitHub Release builds (`YYYY.MM.N`) and allows `repotier update` (`!curOK` in `isNewerVersion`) to upgrade local builds to the latest published release.
ALTERNATIVES: `dev-<short-sha>`; `unstable`; hardcoded `2026.11.1` in `mise.toml`.
DEFERRED: None.
