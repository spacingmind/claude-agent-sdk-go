# CLI Subprocess Robustness (phase 4c)

Closes 3 gaps in `transport.go`'s process handling that the Python
reference has and this port doesn't yet: binary-discovery fallback beyond
a bare `PATH` search, a softer shutdown escalation (SIGTERM before
SIGKILL), and a non-blocking version compatibility check at connect time.

## Acceptance Criteria

1. Binary discovery (`resolveCLIPath` or similar, called from `New()`
   before spawning): if `WithCLIPath` was given, use it as-is (unchanged —
   skip discovery entirely, matching today's behavior). Otherwise: (a)
   `exec.LookPath("claude")` (today's only behavior, keep as the first
   real check); (b) if not found, probe these fixed POSIX paths in order,
   returning the first that exists and is executable:
   `~/.npm-global/bin/claude`, `/usr/local/bin/claude`,
   `~/.local/bin/claude`, `~/node_modules/.bin/claude`,
   `~/.yarn/bin/claude`, `~/.claude/local/claude`; (c) if still not found,
   return a `CLINotFoundError` (from phase 4b if merged first, else a
   plain wrapped error naming the searched locations — see Decisions on
   sequencing) with an actionable message pointing at `npm install
   -g @anthropic-ai/claude-code` and `WithCLIPath`. No Windows-specific
   handling, no bundled-binary fallback (this repo doesn't ship one) —
   both explicitly out of scope.
2. Shutdown escalation in `transport.close()`: replace today's 2-stage
   sequence (stdin-close, wait up to `gracePeriod`, then `Process.Kill()`)
   with 3 stages: stdin-close → wait up to `gracePeriod` (today's default
   5s, unchanged) → if still running, `Process.Signal(syscall.SIGTERM)`
   → wait up to `gracePeriod` again → if still running, `Process.Kill()`
   (SIGKILL) → wait up to `gracePeriod` one more time (errors from this
   final wait are still suppressed, matching today's "expected, process
   died by signal" comment). Total worst case roughly 3x today's grace
   period instead of 1x — document this in the doc comment on `Close`.
   `closeGracePeriod`'s existing semantics (configurable via the
   already-existing but unexported `withCloseGracePeriod` test knob) stay
   as "the wait duration per stage," not "the total budget" — don't
   change its meaning, just apply it three times instead of once.
3. Version check (best-effort, non-blocking): at the end of `New()`,
   after the subprocess is spawned and before/around the `initialize`
   handshake, run `<resolved-cli-path> -v` with a 2-second timeout,
   parse a semver-ish string from its output, and — if it parses and is
   below a `minimumCLIVersion` constant (pick `"2.0.0"` to match the
   Python reference) — write a one-line warning to `o.logWriter` if one
   was configured via `WithLogWriter` (silently skip if none was set,
   since there's no other logging sink in this package). Any failure in
   this probe (binary not found for the `-v` call, timeout, unparseable
   output) is silently swallowed — this must never fail or delay `New()`
   beyond the 2-second cap, and must never block on a hung `-v` invocation
   (use a context with timeout around the `-v` subprocess call, not the
   main transport).

## Test Scenarios

- No `WithCLIPath`, `claude` not on `PATH`, but present at one of the
  well-known fallback paths (use `t.Setenv("PATH", "")` plus a temp dir
  matching one of the probed paths — or, more practically, make the probe
  list itself overridable via an unexported test seam like phase 2's
  `configHomeDir`, so tests don't need to fight real `$HOME`) → `New()`
  succeeds using that path.
- None of PATH or the fallback paths have the binary → `New()` fails with
  an actionable error naming the searched locations.
- `Close()` against a fake CLI that ignores stdin-EOF but exits cleanly on
  SIGTERM → completes within roughly 2 grace periods, not 3 (SIGKILL stage
  never needed) — assert via elapsed time bounds, not exact duration.
- `Close()` against a fake CLI that ignores both stdin-EOF and SIGTERM →
  completes within roughly 3 grace periods via the SIGKILL fallback,
  confirming the process is actually gone afterward.
- Regression: `TestClient_CloseForceKillsHungProcess` and
  `TestClient_CloseIsIdempotent` (existing phase-1 tests) still pass.
- Version-check test: fake CLI's `-v` invocation returns a version below
  the minimum → a warning line appears on a `WithLogWriter`-configured
  buffer; `New()` still succeeds and completes promptly (assert it doesn't
  add meaningful latency — bound the test's total time). No `WithLogWriter`
  configured → no panic, no error, `New()` behaves identically to today.
  Fake CLI's `-v` hangs → `New()` still completes within its normal time
  bounds (the probe's 2s timeout doesn't block construction).

## Decisions

- Sequencing with phase 4b (error hierarchy): if 4b has already merged
  when this phase starts, use `CLINotFoundError` for the discovery-failure
  case (AC 1c); if not, use a plain wrapped error with an equivalent
  message and let a future pass upgrade it — don't block this phase on 4b
  landing first, but prefer 4b's type if it's available.
- No Windows support added here (matching this repo's apparent POSIX-only
  scope so far — no Windows-specific code exists anywhere in the current
  codebase, no CI matrix entry for it) — the Python reference's Windows
  batch-script injection defenses are out of scope entirely, not just
  deferred.
- The version check is deliberately advisory-only (log, don't block or
  error) — matching the Python reference's own choice to treat this as a
  soft compatibility hint, not a hard gate.

## Progress

Complete. Implemented on branch `sdk-parity/cli-subprocess-robustness`:

- AC 1: `discovery.go` — `resolveCLIPath(explicit)` called from `New()`
  before `startTransport` (which now receives the resolved path; the
  default `cliPath: "claude"` was removed so resolution always runs).
  Explicit `WithCLIPath` passes through unchanged. Fallback probe list
  exposed as the `cliFallbackPaths` function-variable seam (same pattern
  as `configHomeDir`). Discovery miss returns
  `&CLINotFoundError{Path: "claude", ...}` naming PATH, every fallback
  location, `npm install -g @anthropic-ai/claude-code`, and
  `WithCLIPath`.
- AC 2: `transport.close()` is now 3 stages — stdin-close + wait →
  SIGTERM + wait → SIGKILL + wait — reusing the single `waitDone`
  reaper (`cmd.Wait` still called exactly once, in `wait()`).
  Corrected during review (2026-08-27, after the implementing agent's
  first pass): `closeErr` is set ONLY on the stage-1 natural-exit path
  (a CLI that exited badly entirely on its own, before any signal was
  sent) — both the SIGTERM and SIGKILL paths suppress the wait error,
  since both are signals *we* sent, not the CLI exiting "on its own".
  The agent's original implementation reported the SIGTERM-stage error
  too, which contradicted the pre-existing `close()` doc comment's own
  stated intent ("an error from being killed by us is not reported");
  fixed directly plus the four tests that had encoded the wrong
  assumption (`TestClient_CloseForceKillsHungProcess`,
  `TestClient_CloseIsIdempotent` in `client_test.go`, the
  close-during-prompt test in `engine_test.go`, and a stale comment in
  `errors_test.go`). Doc comment documents the ~3x gracePeriod worst
  case. `gracePeriod` keeps its per-stage meaning.
- AC 3: `checkCLIVersion` in `discovery.go`, run from `New()` in a
  goroutine right after `startTransport` succeeds: `exec.CommandContext`
  with a 2s timeout, first `\d+\.\d+\.\d+` match compared against
  `minimumCLIVersion = "2.0.0"` by a simple dotted-integer comparator,
  one warning line to `o.logWriter` (nil writer = probe skipped
  entirely). All failures silently swallowed; the context bounds any
  hung `-v` child so nothing leaks.
- Tests: `discovery_test.go` (fallback discovery via the seam, explicit
  passthrough, actionable not-found error, New-level failure, stub
  spawn, SIGTERM-stage close timing, SIGKILL-stage close timing +
  process-gone check, version warn / no-writer / hanging-probe, version
  comparator table). Fake CLI gained `-v` handling
  (`CLAUDECODE_FAKE_VERSION`, incl. "hang") and `sigterm_exit` /
  `ignore_signals` scenarios. Existing close tests updated: the "hang"
  fake now dies at the SIGTERM stage; `Close()` returns nil there (see
  the AC 2 correction above) — assertions in `client_test.go`,
  `engine_test.go`, `errors_test.go` were re-fixed to match the
  corrected (nil-on-SIGTERM-death-too) semantics, not the implementing
  agent's original (error-on-SIGTERM-death) assumption.

## Validation

- `go build -buildvcs=false ./...` — clean.
- `go vet ./...` — clean.
- `gofmt -l .` — no output.
- `go test -race -count=1 ./...` — ok (all tests, including the
  phase-1 regressions `TestClient_CloseForceKillsHungProcess` and
  `TestClient_CloseIsIdempotent`, and the corrected SIGTERM-stage
  assertions), stable across repeated runs.
- No leaked fake-CLI processes after the suite (verified via `ps aux`
  post-run).
