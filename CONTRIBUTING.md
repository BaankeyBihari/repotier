# Contributing to `repotier`

Thank you for contributing to `repotier`! This project uses **Linked-Intent Development (LID)** to keep architecture, component designs, testable specifications, tests, and code strictly aligned.

## Prerequisites

Tools are managed via [mise](https://mise.jdx.dev/) (configured in [mise.toml](mise.toml)):
- **Go `1.27`**
- **`golangci-lint`**
- **`markdownlint-cli2`**
- **`pre-commit`**

Install all tools and register the Git pre-commit hooks:
```bash
mise install
mise run hooks
```

## Common `mise` Tasks

| Command | Description |
|---|---|
| `mise run build` | Build `./repotier` from `./cmd/repotier` with CalVer (`VERSION`) injected |
| `mise run test` | Run the full integration test suite (`go test -timeout 15s ./cmd/repotier/...`) |
| `mise run lint` | Run `gofmt`, `go vet`, and `golangci-lint` |
| `mise run clean` | Remove compiled binaries, coverage artifacts, and Go test cache |
| `mise run install` | Install `repotier` into `$GOPATH/bin` (`~/go/bin`) |

## Linked-Intent Development (LID) Workflow

All code changes — features, refactors, and bug fixes — flow through the arrow of intent in one direction (see [AGENTS.md](AGENTS.md)):

```text
HLD → LLDs → EARS → Tests → Code
```

1. **HLD Check ([docs/high-level-design.md](docs/high-level-design.md)):** Verify whether the change alters top-level system architecture or project tenets.
2. **LLD Update (`docs/intent/<segment>/<segment>-design.md`):** Update the relevant leaf LLD (`cli`, `storage`, `gitops`, or `web`) and record non-obvious trade-offs in its *Decisions & Alternatives* table.
3. **EARS Specs (`docs/intent/<segment>/<segment>-specs.md`):** Add or update structured one-line EARS requirements (`[ ]` while in progress, `[x]` when implemented and tested). Keep spec IDs stable.
4. **Tests First (`cmd/repotier/*_test.go`):** Write or update tests before implementation, annotating each test function with `// @spec <ID>` citing the EARS IDs it verifies.
5. **Implementation (`cmd/repotier/*.go`):** Implement the behavior with minimal, stdlib-first code and place `// @spec <ID>` annotations at the entry point of each behavior's implementation graph.

## Pre-Commit & Pull Request Checklist

- All `gofmt`, `go vet`, and `golangci-lint` checks pass (`mise run lint`).
- All tests pass (`mise run test`).
- Every active `[x]` EARS spec is cited by `@spec` annotations in both production code and `*_test.go`.
- Docs read cold (present intent only, no conversational residue).
