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
