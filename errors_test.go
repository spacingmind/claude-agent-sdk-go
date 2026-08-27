package claudecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNew_CLINotFound(t *testing.T) {
	t.Parallel()

	_, err := New(t.TempDir(), WithCLIPath(filepath.Join(t.TempDir(), "no-such-claude")))
	if err == nil {
		t.Fatal("New() with nonexistent CLI path = nil error, want error")
	}

	if _, ok := errors.AsType[*CLINotFoundError](err); !ok {
		t.Fatalf("New() error = %T (%v), want *CLINotFoundError", err, err)
	}
}

func TestNew_CLINotFoundNotExecutable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := New(t.TempDir(), WithCLIPath(path))
	if err == nil {
		t.Fatal("New() with non-executable CLI path = nil error, want error")
	}

	if _, ok := errors.AsType[*CLINotFoundError](err); !ok {
		t.Fatalf("New() error = %T (%v), want *CLINotFoundError", err, err)
	}
}

func TestNew_ConnectionErrorOnFailedHandshake(t *testing.T) {
	t.Parallel()

	_, err := New(t.TempDir(), fakeCLIOptions(t, "exit_immediately")...)
	if err == nil {
		t.Fatal("New() with CLI exiting pre-handshake = nil error, want error")
	}

	if _, ok := errors.AsType[*CLIConnectionError](err); !ok {
		t.Fatalf("New() error = %T (%v), want *CLIConnectionError", err, err)
	}
}

func TestClient_ErrNilAfterCleanClose(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "hang")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// The fake "hang" CLI exits at close's SIGTERM stage; Err() must stay
	// nil for any Close()-initiated shutdown, regardless of which stage.
	_ = c.Close()

	// Wait for the stream to actually end so Err() reflects the final
	// state, not just the close signal.
	for range c.ReceiveMessages(context.Background()) { //nolint:revive  // intentional drain until the stream closes, no per-message action needed
	}

	if err := c.Err(); err != nil {
		t.Fatalf("Err() after clean Close() = %v, want nil", err)
	}
}

func TestClient_ErrProcessErrorAfterCrash(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "crash")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	// Kick off a turn so the crash happens mid-turn; drain the stream so
	// readLoop is never blocked on a full c.msgs buffer.
	updates := make(chan Message, 100)
	promptDone := make(chan error, 1)

	go func() {
		_, err := c.Prompt(context.Background(), "hi", updates)
		promptDone <- err
	}()

	for range c.ReceiveMessages(context.Background()) { //nolint:revive  // intentional drain until the stream closes, no per-message action needed
	}

	select {
	case <-promptDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Prompt() after crash")
	}

	err = c.Err()
	if err == nil {
		t.Fatal("Err() after crash = nil, want error")
	}

	if _, ok := errors.AsType[*ProcessError](err); !ok {
		t.Fatalf("Err() after crash = %T (%v), want *ProcessError", err, err)
	}

	if _, ok := errors.AsType[*ResultError](err); ok {
		t.Fatalf("Err() after crash without error result = %T, ProcessError and ResultError must be mutually exclusive", err)
	}
}

func TestClient_ErrResultErrorAfterErroredResultThenCrash(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "crash_result_error")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message, 100)
	promptDone := make(chan error, 1)

	go func() {
		_, err := c.Prompt(context.Background(), "hi", updates)
		promptDone <- err
	}()

	for range c.ReceiveMessages(context.Background()) { //nolint:revive  // intentional drain until the stream closes, no per-message action needed
	}

	select {
	case <-promptDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Prompt() after crash")
	}

	err = c.Err()
	if err == nil {
		t.Fatal("Err() after errored-result crash = nil, want error")
	}

	var resErr *ResultError
	if !errors.As(err, &resErr) {
		t.Fatalf("Err() = %T (%v), want *ResultError", err, err)
	}

	if !resErr.Result.IsError {
		t.Fatalf("ResultError.Result.IsError = false, want true")
	}

	if resErr.Text != "err-one; err-two" {
		t.Fatalf("ResultError.Text = %q, want errors joined", resErr.Text)
	}
}

func TestClient_CLIJSONDecodeErrorOnMalformedStatusPayload(t *testing.T) {
	t.Parallel()

	opts := fakeCLIEnvOpts(t, "control_echo",
		`CLAUDECODE_FAKE_RESPONSES={"mcp_status":{"mcpServers":"not-an-array"},`+
			`"get_context_usage":{"categories":"nope"}}`)

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx := context.Background()

	_, err = c.GetMCPStatus(ctx)
	if err == nil {
		t.Fatal("GetMCPStatus() with malformed payload = nil error, want error")
	}

	var decErr *CLIJSONDecodeError
	if !errors.As(err, &decErr) {
		t.Fatalf("GetMCPStatus() error = %T (%v), want *CLIJSONDecodeError", err, err)
	}

	_, err = c.GetContextUsage(ctx)
	if err == nil {
		t.Fatal("GetContextUsage() with malformed payload = nil error, want error")
	}

	if !errors.As(err, &decErr) {
		t.Fatalf("GetContextUsage() error = %T (%v), want *CLIJSONDecodeError", err, err)
	}
}
