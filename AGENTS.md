# AGENTS.md

Repository protocol entrypoint for agents working in this repo.

## Project overview

See [README.md](README.md) for what this package is and how it's used.

## Workspace map

- `client.go`, `messages.go`, `permission.go`, `transport.go` — the package
  (single Go package at repo root, `package claudecode`).
- `*_test.go` — tests, including a fake-CLI harness (`fakecli_test.go`) that
  stands in for the real `claude` binary in tests.
- `docs/` — ADRs (`docs/decisions/`) and active/completed plans
  (`docs/plans/`).
- `refs/` — read-only reference clones of other projects for pattern lookup
  (see below). Gitignored; not part of this repo's history.

## Workflow rules

**(a) Read-only questions.** Inspect the smallest relevant surface for the
question, then answer with evidence (file paths, line numbers, quoted code).
Don't guess at behavior you haven't read.

**(b) Bounded changes.** Make the smallest coherent change that satisfies the
request. Run the `verify` skill (`go build`, `go test -race`, `go vet`,
`gofmt`) before considering the change done.

**(c) Multi-session work — spec-driven.** Before implementation starts,
create `docs/plans/active/<slug>.md` with concrete acceptance criteria and
named test scenarios (the spec), plus sections for decisions, progress, and
validation (how the spec got satisfied). See the `plan` skill. Keep it
updated as work proceeds. When finished — every acceptance criterion
confirmed in Validation — move the file to `docs/plans/completed/`.

**(d) Material ambiguity.** If a choice materially affects public API shape,
wire-protocol behavior, or how closely this package tracks the upstream
Python/TypeScript SDKs, and isn't already decided in `docs/decisions/`, STOP
and present the choice to the user. Do not decide architecture unilaterally.

## Branching & release workflow

- `main` is release-only. Do not commit or push directly to `main` going
  forward (this repo did during the initial 1:1 parity port — that
  history predates this rule, not an exception to it).
- `develop` is the integration branch. Feature/fix work happens on
  short-lived branches off `develop`, merged back via PR.
- Releasing means opening a PR from `develop` into `main`. Merging that PR
  is what triggers everything downstream:
  1. `.github/workflows/release-please.yml` (on push to `main`) runs
     [release-please](https://github.com/googleapis/release-please),
     which reads Conventional Commits since the last release and either
     opens/updates a `chore(main): release X.Y.Z` PR (version bump +
     `CHANGELOG.md`), or — if that release PR is what just got merged —
     creates the `vX.Y.Z` git tag directly.
  2. Pushing that tag triggers `.github/workflows/release.yml`, which runs
     `goreleaser` to build the actual GitHub Release (changelog grouping,
     `pkg.go.dev` links). `release-please` also creates a plain release
     for the tag, but `goreleaser`'s `release.mode: replace` overwrites it
     with the nicer-formatted one — deliberately NOT using
     `skip-github-release`, since that flag breaks release-please's own
     tag-tracking (see `googleapis/release-please#1561`).
- **Commit messages merged into `main` must follow [Conventional
  Commits](https://www.conventionalcommits.org/)** (`feat:`, `fix:`,
  `feat!:`/`BREAKING CHANGE:` footer for breaking changes, `chore:`/
  `docs:`/`ci:`/`test:` for everything release-please should exclude from
  the changelog) — release-please cannot determine the correct version
  bump or changelog entry without this. This matters most for the PR
  title/squash-commit message that actually lands on `main`, not
  necessarily every commit on the feature branch.

## refs/ map

`refs/` holds read-only clones of the official Anthropic SDKs, used as the
spec for this port — not dependencies, not code to copy wholesale (Python
and Go have different idioms; port behavior and wire-protocol shapes, not
syntax).

- `refs/claude-agent-sdk-python` — source of truth for wire-protocol
  behavior, control-request/response shapes, and session lifecycle
  semantics. Primary reference for the 1:1 parity effort.
- `refs/claude-agent-sdk-typescript` — secondary reference, consulted when
  the Python SDK's behavior is ambiguous or when a design reads more
  naturally from the TS side.
- `refs/claude-code` — the `claude` CLI itself, for wire-protocol edge cases
  neither SDK's source fully documents.

Day-to-day Go style is enforced by linters (`go vet`/`gofmt`) and the
`golang-*` skills, not by reading refs/. Reach for refs/ when porting a
specific behavior (session resume, MCP bridge control requests, message
parsing) where the exact shape matters.
