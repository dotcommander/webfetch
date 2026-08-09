# Repository Guidelines

## Project Structure & Module Organization

The executable lives in `cmd/webfetch/`; keep CLI parsing, output rendering, protocol, and MCP adapter code there. Core fetch, search, cache, reader, schema, and provider behavior belongs in `internal/webfetch/`. Place package tests beside their source as `*_test.go`; reusable fixtures belong in `internal/webfetch/testdata/`. Protocol documentation is under `docs/`, and `scripts/mcp-smoke-test.py` exercises the installed MCP binary.

## Build, Test, and Development Commands

- `just build` builds `./cmd/webfetch` into the root-level `webfetch` binary and embeds the Git-derived version.
- `just test` runs the full Go suite with the race detector (`go test -race ./...`).
- `go test ./internal/webfetch` runs the core package tests during focused development.
- `go test ./cmd/webfetch -run TestName` runs a specific CLI or MCP test.
- `go vet ./...` checks common Go correctness issues.
- `just --list` shows the maintained recipes; `just clean` removes only the local binary.

## Coding Style & Naming Conventions

Use standard Go formatting: run `gofmt` on every changed `.go` file and let it manage tabs and imports. Keep package names short and lowercase, exported identifiers in `PascalCase`, and internal identifiers in `camelCase`. Follow the existing explicit control flow and small-helper style. Keep provider-specific behavior behind the service abstractions in `internal/webfetch`, and preserve stable JSON, error-code, CLI, and MCP contracts unless the change intentionally updates them.

## Testing Guidelines

Use Go's `testing` package, table-driven cases, `httptest`, and `t.Parallel()` where isolation permits. Name tests `TestBehaviorCondition`. Behavior changes require focused regression coverage; there is no stated numeric coverage threshold. Tests must not depend on production credentials or uncontrolled public network calls. For MCP changes, also follow `docs/mcp-smoke-test.md` and run its installed-binary checks when applicable.

## Commit & Pull Request Guidelines

Recent history uses concise Conventional Commit subjects such as `feat: add opt-in URL cache`, `test: add MCP smoke suite`, and `docs: document MCP 2.0 integration`. Keep each commit single-purpose. Pull requests should explain user-visible behavior and compatibility impact, link the relevant issue, and list exact verification commands. Include sample CLI or JSON output for protocol changes; screenshots are unnecessary for this command-line project.

## Security & Configuration

Never commit `JINA_API_KEY`, `BRAVE_API_KEY`, or `EXA_API_KEY`. Preserve private-network and redirect validation when changing HTTP behavior. Use local fake providers for tests and document any new environment variable in `README.md`.
