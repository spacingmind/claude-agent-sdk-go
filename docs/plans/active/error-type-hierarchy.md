# Error Type Hierarchy (phase 4b)

Closes the gap flagged (but deferred) in phase 1's Decisions: this
package currently only returns plain wrapped `fmt.Errorf` errors
everywhere. The Python reference has a typed exception hierarchy
(`ClaudeSDKError` base; `CLIConnectionError`, `CLINotFoundError`,
`ProcessError`, `ResultError`, `CLIJSONDecodeError`) callers can catch
selectively. This phase adds Go equivalents callers can `errors.As`
against, and wraps the existing call sites where each one naturally
applies — without changing this repo's already-decided "skip malformed
lines rather than fail the turn" policy in `decodeLine`.

## Acceptance Criteria

1. New file `errors.go` defines:
   ```go
   type CLINotFoundError struct { Path string; Err error }       // claude binary not found / not executable
   type CLIConnectionError struct { Err error }                   // subprocess spawn or transport connect failure
   type ProcessError struct { ExitCode int; Stderr string; Err error } // subprocess exited unexpectedly
   type ResultError struct { Result ResultMessage; Text string }  // a turn ended with ResultMessage.IsError true and the process then died/the stream terminated abnormally
   type CLIJSONDecodeError struct { Data string; Err error }      // a control-response/status payload failed to decode (NOT per-line message decode -- see Decisions)
   ```
   Each implements `Error() string` (a clear, actionable message including
   the wrapped context) and `Unwrap() error` (returning `Err` where
   present), so `errors.As(err, &claudecode.ProcessError{})` and
   `errors.Is`/wrapped-chain inspection both work per stdlib convention.
2. `CLINotFoundError` is returned wherever subprocess spawn fails because
   the binary couldn't be found/executed (`transport.go`'s `startTransport`,
   wrapping the `exec.Command`/`cmd.Start()` failure when the underlying
   OS error is `exec.ErrNotFound` or a permission/not-exist condition —
   distinguish this from other `cmd.Start()` failures, which stay
   `CLIConnectionError`).
3. `CLIConnectionError` wraps any other transport-connect-time failure in
   `New()` (pipe creation failure, `cmd.Start()` failure not matching #2,
   the `initialize` control-request handshake failing).
4. `ProcessError` is what `readLoop`'s force-fail path (phase 1's AC 9)
   produces when the transport's line stream ends because the subprocess
   exited unexpectedly (not via a clean `Close()`) — carry the process's
   exit code (if obtainable) and any captured stderr tail if the transport
   has one buffered (it's fine if stderr isn't captured today and this
   field is just often empty — don't add new stderr-buffering machinery
   for this, that's out of scope).
5. `ResultError` is produced by the same `readLoop` force-fail path
   specifically when the last message seen before the stream ended was a
   `ResultMessage` with `IsError: true` — carry that `ResultMessage` and a
   best-effort human-readable `Text` derived from it (prefer
   `Result.Errors` if non-empty, else `Result.Result`, else
   `Result.StopReason`, else a generic fallback string). When this
   condition applies, return `ResultError` instead of the generic
   `ProcessError` for that same failure (mutually exclusive, not both).
6. `CLIJSONDecodeError` is used where `GetMCPStatus`/`GetContextUsage`
   (phase 1's `engine.go`) fail to unmarshal a control response's
   `response` payload into their typed result structs — this is a
   different failure class from `decodeLine`'s per-message-line handling,
   which stays exactly as-is (skip malformed lines, no error type
   involved) per this repo's already-established policy; do not touch
   `decodeLine`/`messages.go`'s error behavior in this phase.
7. All error-returning call sites elsewhere in the package
   (`sessions.go`, `mcp.go`, option-validation in `client.go`) are
   **unaffected** — this phase only wraps the specific transport/process/
   result-lifecycle failures listed above, not every error in the package.
   Don't go on a broad refactoring pass; scope is exactly what's listed.

## Test Scenarios

- Spawning with a `WithCLIPath` pointing at a nonexistent file →
  `New()`'s error satisfies `errors.As(err, &CLINotFoundError{})`.
- A fake-CLI scenario that closes stdout immediately without a clean
  handshake → the resulting error (from `Prompt`/`ReceiveMessages`)
  satisfies `errors.As(err, &CLIConnectionError{})` if it happens during
  `New()`, or the appropriate process/result error if it happens after a
  successful connect (see next two scenarios).
- A fake-CLI scenario that exits abruptly mid-turn (no `Close()` called by
  the test) with a preceding `IsError:false` state → the stream-termination
  error satisfies `errors.As(err, &ProcessError{})`.
- A fake-CLI scenario whose last message before exiting is a `ResultMessage`
  with `IsError:true` → the stream-termination error satisfies
  `errors.As(err, &ResultError{})` instead, and `ResultError.Result.IsError`
  is `true`.
- `GetMCPStatus` against a fake CLI that returns a malformed (non-JSON or
  wrong-shape) `mcp_status` response → the returned error satisfies
  `errors.As(err, &CLIJSONDecodeError{})`.
- Regression: `decodeLine`'s existing malformed-line tests
  (`TestDecodeLine_MalformedLinesStillError`) still pass unchanged — this
  phase must not alter that behavior.

## Decisions

- Scoped narrowly to the 5 error types and their most natural existing
  call sites, not a package-wide error-handling refactor — the audit that
  found this gap flagged it as "callers can't `errors.As` against
  anything specific today," and that's exactly what this phase fixes,
  without expanding scope into every `fmt.Errorf` in the codebase.
- `CLIJSONDecodeError`'s scope is deliberately narrow (control-response
  payload decoding only) specifically to avoid touching `decodeLine`'s
  established "skip malformed message lines" policy, which this repo
  already chose intentionally in phase 1 and doesn't need relitigating.

## Progress

Complete. Implemented on `sdk-parity/error-hierarchy`:

- `errors.go`: the five types (`CLINotFoundError`, `CLIConnectionError`,
  `ProcessError`, `ResultError`, `CLIJSONDecodeError`) with `Error()` and
  `Unwrap()` (where an `Err` field exists), plus `resultErrorText` (the
  Errors-joined / Result / StopReason / fallback Text derivation).
- `transport.go`: `startTransport` classifies `cmd.Start()` failures —
  `exec.ErrNotFound` in the chain or an `os.PathError` with
  not-exist/permission becomes `CLINotFoundError`; pipe-creation and other
  start failures become `CLIConnectionError`. Added a single waiter
  goroutine (`transport.wait`) that reaps the subprocess once and publishes
  its exit code (`exitCode`, -1 until reaped) and wait error; `close` now
  consumes that result instead of calling `cmd.Wait()` itself (a second
  `Wait` on the same `Cmd` is a data race). Added a race-free
  `transport.exited()` used by the persistence test that previously read
  `cmd.ProcessState` directly.
- `client.go`: the failed `initialize` handshake in `New()` wraps as
  `CLIConnectionError`. Added `streamErr` field (RWMutex-guarded).
- `engine.go`: `readLoop` tracks the last forwarded `ResultMessage` and, on
  an abnormal stream end (transport closed or read error, not `Close()`),
  records `ResultError` (if that result had `IsError: true`) or
  `ProcessError` (carrying the reaped exit code) via `setStreamErr`; clean
  `Close()` leaves it nil. `GetMCPStatus`/`GetContextUsage` payload decode
  failures wrap as `CLIJSONDecodeError`. `decodeLine`/`messages.go`
  untouched.
- `Client.Err()` (addition beyond the original AC list — see Decisions):
  follows the `bufio.Scanner.Err()`/`sql.Rows.Err()` convention so
  `ReceiveMessages`/`ReceiveResponse` consumers can distinguish a clean
  stream end (`Close()`, nil) from an abnormal one after the channel
  closes; without it, `ProcessError`/`ResultError` were unreachable by
  callers. Safe to call from any goroutine.
- `fakecli_test.go`: new scenarios `crash_result_error` (errored result
  then exit) and `exit_immediately` (exits before initialize).
- `errors_test.go`: CLINotFound (missing path, non-executable file),
  CLIConnection on failed handshake, Err() nil after clean Close, Err() as
  ProcessError after mid-turn crash, Err() as ResultError after errored
  result + crash (with Text assertion), CLIJSONDecodeError for malformed
  mcp_status/get_context_usage payloads.

## Decisions (added this phase)

- `Client.Err()` was added beyond the plan doc's AC 1-7: the review that
  produced this phase found that `readLoop` closed `c.msgs` with no error
  signal either way, making `ProcessError`/`ResultError` (AC 4-5)
  unreachable by stream consumers. `Err()` is the minimal, convention-
  matching surface that makes them observable.

## Validation

All commands run on `sdk-parity/error-hierarchy`, all clean:

- `go build -buildvcs=false ./...` — ok.
- `go test -race -count=1 ./...` — ok (11.4s). Two races found and fixed
  along the way: (1) my initial second `cmd.Wait` in a reaper goroutine
  raced `close`'s own `Wait` — fixed by the single-waiter design described
  in Progress; (2) `TestClient_SequentialPromptsReuseSubprocess` read
  `cmd.ProcessState` directly, now racing the waiter goroutine — switched
  to the new race-free `transport.exited()`.
- `go vet ./...` — clean. `gofmt -l .` — no output.
- Zero leaked fake-CLI processes after the run
  (`ps aux | grep claude-agent-sdk-go.test` empty).

Acceptance criteria 1-7 confirmed: 1 (errors.go types), 2 (CLINotFound
classification + tests), 3 (CLIConnectionError on pipes/start/handshake +
test), 4/5 (mutually exclusive ProcessError/ResultError via `Err()`,
tests assert both directions), 6 (CLIJSONDecodeError in both status
methods, decodeLine untouched), 7 (no broad refactor — `sessions.go`,
`mcp.go`, option validation untouched). All Test Scenarios covered by
`errors_test.go`; the regression scenario is covered by the existing
`TestDecodeLine_MalformedLinesStillError` passing unchanged. Plan doc
retained in `docs/plans/active/` (move to completed happens at merge time,
matching how earlier phases handled it).
