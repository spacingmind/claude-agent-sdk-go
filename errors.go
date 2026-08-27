package claudecode

import (
	"fmt"
	"strings"
)

// CLINotFoundError reports that the claude CLI binary itself could not be
// found or executed. Path is the binary path the client tried to spawn.
type CLINotFoundError struct {
	Path string
	Err  error
}

func (e *CLINotFoundError) Error() string {
	return fmt.Sprintf("claudecode: CLI binary not found or not executable at %q: %v", e.Path, e.Err)
}

func (e *CLINotFoundError) Unwrap() error { return e.Err }

// CLIConnectionError reports a failure to connect to the CLI subprocess at
// construction time: pipe creation, process start (other than a missing
// binary, which is CLINotFoundError), or a failed initialize handshake.
type CLIConnectionError struct {
	Err error
}

func (e *CLIConnectionError) Error() string {
	return fmt.Sprintf("claudecode: failed to connect to CLI: %v", e.Err)
}

func (e *CLIConnectionError) Unwrap() error { return e.Err }

// ProcessError reports that the CLI subprocess ended the message stream on
// its own (crashed or exited) rather than via a clean Client.Close.
// ExitCode is -1 when it could not be observed. This package does not
// buffer the CLI's stderr, so Stderr is typically empty.
type ProcessError struct {
	ExitCode int
	Stderr   string
	Err      error
}

func (e *ProcessError) Error() string {
	msg := fmt.Sprintf("claudecode: CLI process exited unexpectedly (exit code %d)", e.ExitCode)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}

	if e.Err != nil {
		msg += fmt.Sprintf(": %v", e.Err)
	}

	return msg
}

func (e *ProcessError) Unwrap() error { return e.Err }

// ResultError reports that the stream terminated abnormally right after the
// CLI sent a ResultMessage with IsError true. Text is a best-effort
// human-readable summary derived from that result.
type ResultError struct {
	Result ResultMessage
	Text   string
}

func (e *ResultError) Error() string {
	return fmt.Sprintf("claudecode: CLI turn ended with an error result: %s", e.Text)
}

// CLIJSONDecodeError reports that a control-response payload failed to
// decode into its typed struct. Data is the raw payload that failed.
// Per-message-line decoding is deliberately out of scope: malformed message
// lines are skipped, not errored (see decodeLine).
type CLIJSONDecodeError struct {
	Data string
	Err  error
}

func (e *CLIJSONDecodeError) Error() string {
	return fmt.Sprintf("claudecode: failed to decode control response payload %q: %v", e.Data, e.Err)
}

func (e *CLIJSONDecodeError) Unwrap() error { return e.Err }

// resultErrorText derives ResultError.Text: prefer the result's Errors
// list, then its Result text, then StopReason, else a generic fallback.
func resultErrorText(r ResultMessage) string {
	switch {
	case len(r.Errors) > 0:
		return strings.Join(r.Errors, "; ")
	case r.Result != "":
		return r.Result
	case r.StopReason != "":
		return r.StopReason
	default:
		return "the CLI reported an error result"
	}
}
