# Exit status and stdout drain

## Acceptance Criteria

1. Every complete stdout line the CLI wrote before exiting is delivered on the message stream before it closes.
2. After the stream ends on its own, the exit code and terminating signal are available (`ProcessError.ExitCode/Signal`, `Client.ExitStatus()`), with no race between stdout EOF and `cmd.Wait`.
3. stderr is drained (bounded) before the exit status is published; its tail is in `ProcessError.Stderr` / `ExitStatus.Stderr`.
4. Additive API only.
5. A descendant that inherits the CLI's stdout and outlives it must not hang the stream or the exit status: all messages delivered, stream ends and status is available within seconds.

## Test Scenarios

- `TestExitStatus_BurstThenExitDeliversAllAndExitCode` (200 msgs, exit 3)
- `TestExitStatus_BurstThenSignalReportsSignal` (200 msgs, SIGKILL)
- `TestExitStatus_StderrDrainedBeforeExitError` (stderr then exit 4)
- `TestExitStatus_ResultErrorStillExposesExitStatus`
- `TestExitStatus_DescendantHoldingStdoutDoesNotHangStream` (200 msgs, exit 3, background `sleep` holds stdout; ends < 5s)
- `TestExitStatus_LoadNoLoss` (200 runs under 2xNumCPU CPU-burner goroutines, self-joined)

## Decisions

Root cause: `transport.wait()` called `cmd.Wait()` concurrently with the stdout reader; Wait closes the pipe's read end on process exit, discarding unread bytes. First fix (block Wait on stdout EOF) regressed when a descendant holds stdout open (EOF never comes). Final design: the transport owns the stdout pipe (`os.Pipe`, so `cmd.Wait` never closes our read end). The process is reaped immediately on exit (single reaper), independent of stdout EOF; the reader then drains buffered bytes and ends at EOF, or after `stdoutIdleGrace` (500ms) of an empty-pipe idle read via a read deadline refreshed before each read (a slow consumer never trips it). No x/sys needed. Unix only for the deadline (Windows pipes are not pollable; descendant case unbounded there).

## Progress / Validation

All criteria confirmed: `go test -race ./...` passes; the load test passes 200/200 and the descendant scenario passes under -race; with the reader-wait removed the load test fails (e.g. 103/200 messages delivered).
