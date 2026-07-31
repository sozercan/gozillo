# AGENTS.md

## Scope

This file applies to the entire repository. Add a nested `AGENTS.md` only when a
subtree needs more-specific rules; the closest file takes precedence. Keep this
file concise and actionable, and link to focused documentation instead of
copying large design notes here.

## Project overview

`gozillo` is a pure-Go CLI for exploring Zillow listings, fetching normalized
property details, managing browser-derived sessions, processing HAR files, and
producing table, JSON, or JSONL output.

Zillow endpoints are private and unsupported. Preserve the project's bounded,
conservative request behavior, partial-result handling, and explicit security
boundaries. Read `README.md` and `docs/request-discovery.md` before changing
transport, session, search, discovery, property parsing, or HAR capture logic.

## Repository map

- `cmd/gozillo/main.go`: thin executable entry point.
- `internal/cli/`: command parsing, orchestration, retries, caching, progress,
  filters, and stdout/stderr behavior.
- `internal/zillow/`: Zillow client, models, search/discovery, property parsing,
  rental facts, snapshots, and history.
- `internal/httpclient/`: `tls-client` transport adapter and proxy handling.
- `internal/har/`: HAR loading, sanitization, ranking, and profile derivation.
- `internal/cdphar/`: Chromium CDP capture and HAR recording.
- `internal/session/`: minimal session import and owner-only storage.
- `internal/output/`: deterministic table, JSON, and JSONL rendering.
- `internal/har/testdata/`: synthetic/sanitized fixtures and golden files.
- `scripts/`: local helper scripts; keep Bash scripts strict and portable.

## Build, test, and lint

Go 1.24.1 or newer is required.

- `make deps`: download dependencies.
- `make build`: build `./gozillo` from `./cmd/gozillo`.
- `make test`: run the unit test suite.
- `make test-race`: run tests with the race detector.
- `make fmt`: format Go source.
- `make fmt-check`: verify formatting without modifying files.
- `make vet`: run `go vet ./...`.
- `make scripts-check`: validate shell syntax.
- `make data-boundary`: reject tracked raw captures and generated reports.
- `make check`: run the local CI-equivalent validation suite.
- `make ci`: download dependencies and run the full validation suite.

During iteration, run focused tests such as `go test ./internal/zillow` or
`go test ./internal/cli -run TestName`. After dependency changes, run
`make tidy` and then `make ci`.

## Engineering conventions

- Run `gofmt` on every changed Go file.
- Keep `cmd/gozillo/main.go` thin. Implement commands through
  `internal/cli.Command` and register them in `internal/cli/default.go`.
- Preserve the stream contract: normal and machine-readable results go to
  stdout; diagnostics and progress go to stderr. Never contaminate JSON/JSONL
  output with status messages.
- Help text and structured output are deterministic and often asserted exactly.
  Update tests when changing commands, flags, columns, help, or JSON shapes.
- Follow existing Go patterns: early validation, contextual `%w` error wrapping,
  `context.Context` for network operations, and comments on exported APIs.
- Keep tests beside their package. Prefer table-driven tests, `httptest`,
  synthetic fixtures, and exact comparisons. Tests must not depend on live
  Zillow requests, real browser sessions, or real captures.
- Update golden files intentionally and inspect their diffs for sensitive data.
- Preserve bounded retries, request pacing, response-size limits, host checks,
  context cancellation, and partial-success semantics.
- Keep Bash scripts on `#!/usr/bin/env bash` with `set -euo pipefail`, quoted
  variables, validated inputs, and environment-variable overrides.

## Security and data boundaries

- Never commit raw HARs, cookies, authorization values, session files, browser
  profiles, client hints, fingerprints, CAPTCHA values, real addresses, tracking
  identifiers, or generated search reports.
- Checked-in HAR fixtures must be synthetic or sanitized and contain only the
  request shape required by tests.
- Preserve owner-only permissions (`0600` files and `0700` directories) for
  sessions, caches, and sensitive captures.
- Preserve loopback-only CDP discovery and explicit opt-in for trusted remote
  WebSocket endpoints.
- Do not add CAPTCHA solving, automatic proxy rotation, authentication bypasses,
  rate-limit bypasses, or a standard-transport fallback for live requests.
- Do not commit ignored artifacts such as `/gozillo`, `coverage.out`, `/reports/`,
  `*.raw.har`, `captures/raw/`, `.gozillo/`, or `.DS_Store`.
- New files under the ignored `examples/` path require deliberate review and
  sanitization before being force-added.

## Git and pull requests

- Use Conventional Commit style for commit messages and PR titles, for example
  `feat: add task timeline` or `fix(cli): handle empty filters`.
- Sign commits with `git commit -s`.
- Do not prefix PR titles with `[codex]`.
- Open PRs ready for review unless the user explicitly requests a draft.
- Keep changes focused. Update `README.md` or `docs/request-discovery.md` when
  user-facing behavior or documented transport/session invariants change.

## Definition of done

- Add or update focused tests for changed behavior.
- Run the narrowest relevant tests while iterating.
- Before completing Go or shell changes, run `make check`; use `make ci` when
  dependencies changed or a clean-environment check is needed.
- For CLI/output changes, verify exact stdout, stderr, help, and structured-output
  contracts.
- For HAR, session, CDP, or transport changes, verify sanitization, permissions,
  host validation, cancellation, and the repository data boundary.
- Review `git diff` for accidental generated files, secrets, and unrelated edits.
- Report any validation command that could not be run and why.
