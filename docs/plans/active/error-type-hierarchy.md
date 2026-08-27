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

Not started.

## Validation

Not yet applicable.
