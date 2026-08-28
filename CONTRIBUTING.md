# Contributing

Thanks for your interest in `claude-agent-sdk-go`. This is a small,
early-stage project (1-2 maintainers, no formal governance) — the process
below is intentionally lightweight.

## Building and testing locally

```sh
go build ./...          # or: make build
go test -race ./...     # or: make test
go vet ./...             # or: make vet
gofmt -l .               # should print nothing; or: make fmt-check
golangci-lint run        # or: make lint
```

`make check` runs build, vet, `gofmt` check, lint, and test together — the
same sequence CI runs. Run it (or the `verify` skill, if you're an agent
working in this repo) before opening a PR.

## Branching model

- **`main` is release-only.** It only receives merges via PR from
  `develop`, and those merges trigger `release-please` (version bump +
  changelog from Conventional Commits) followed by `goreleaser` (the
  actual GitHub Release). Don't open PRs directly against `main`.
- **`develop` is the integration branch.** Base your feature/fix branch on
  `develop` and open your PR against `develop`.

## Conventional Commits

PRs are squash-merged, so **the PR title becomes the commit message that
lands in the repo's history** — and eventually, when `develop` is released
into `main`, the changelog entry `release-please` generates. PR titles
must follow [Conventional Commits](https://www.conventionalcommits.org/):

- `feat: ...` — a new feature
- `fix: ...` — a bug fix
- `feat!: ...` or a `BREAKING CHANGE:` footer — a breaking API change
- `chore: ...`, `docs: ...`, `ci: ...`, `test: ...` — maintenance work
  release-please excludes from the changelog

Individual commits on your feature branch don't need to follow this strictly,
but the PR title does.

## Plan docs for nontrivial changes

This repo practices spec-driven development (see `AGENTS.md`, rule (c)).
For any change that spans more than a quick, obviously-bounded edit, write a
plan first: create `docs/plans/active/<slug>.md` with concrete acceptance
criteria and named test scenarios before starting implementation, keep it
updated as work proceeds, and move it to `docs/plans/completed/` once every
acceptance criterion is validated. Small, self-contained fixes don't need
one — use your judgment, and see `AGENTS.md` for the full rule.

If a change materially affects the public API shape, the wire-protocol
behavior, or how closely this package tracks the upstream Python/TypeScript
SDKs, please open an issue or discuss before investing in a large PR —
see `AGENTS.md` rule (d).

## Opening a PR

- Target `develop`, not `main`.
- Give the PR a Conventional Commits-style title.
- Fill out the PR template checklist.
