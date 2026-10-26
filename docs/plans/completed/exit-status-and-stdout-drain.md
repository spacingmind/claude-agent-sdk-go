# Exit status and stdout drain

## Acceptance Criteria

1. Every complete stdout line the CLI wrote before exiting is delivered on the message stream before it closes.
2. After the stream ends on its own, the exit code and terminating signal are available (`ProcessError.ExitCode/Signal`, `Client.ExitStatus()`), with no race between stdout EOF and `cmd.Wait`.
3. stderr is drained (bounded) before the exit status is published; its tail is in `ProcessError.Stderr` / `ExitStatus.Stderr`.
4. Additive API only.

## Test Scenarios

- `TestExitStatus_BurstThenExitDeliversAllAndExitCode` (200 msgs, exit 3)
- `TestExitStatus_BurstThenSignalReportsSignal` (200 msgs, SIGKILL)
- `TestExitStatus_StderrDrainedBeforeExitError` (stderr then exit 4)
- `TestExitStatus_ResultErrorStillExposesExitStatus`
- `TestExitStatus_LoadNoLoss` (200 runs under 2xNumCPU CPU-burner goroutines, self-joined)

## Decisions

Root cause: `transport.wait()` called `cmd.Wait()` concurrently with the stdout reader; Wait closes the pipe's read end on process exit, discarding unread bytes. Fix: the waiter blocks on reader completion (or transport close) first.

## Progress / Validation

All criteria confirmed: `go test -race ./...` passes; the load test passes 200/200; with the reader-wait removed the load test fails (e.g. 103/200 messages delivered).
