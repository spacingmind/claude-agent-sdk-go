package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestClient_PIDIsTheSpawnedCLIsOwnProcess(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "capture")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	// Valid as soon as New returns, before any turn.
	pid := c.PID()
	if pid <= 1 {
		t.Fatalf("PID() = %d right after New, want the CLI's process id", pid)
	}

	// The fake CLI reports its OWN pid through the result message: the client's
	// PID() must be that process, not a wrapper and not this test binary.
	res, err := c.Prompt(context.Background(), "hi", make(chan Message))
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var dump struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(res.Result), &dump); err != nil {
		t.Fatalf("unmarshal captured dump %q: %v", res.Result, err)
	}

	if dump.PID != pid {
		t.Fatalf("PID() = %d, but the CLI reports its own pid as %d", pid, dump.PID)
	}

	if c.PID() != pid {
		t.Fatalf("PID() changed between calls: %d then %d", pid, c.PID())
	}
}

func TestClient_PIDIsNotTheCallersOwn(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "hang")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.PID() == os.Getpid() {
		t.Fatal("PID() is this process's own pid: it must be the subprocess's")
	}
}

func TestClient_PIDOfTwoClientsDiffer(t *testing.T) {
	t.Parallel()

	a, err := New(t.TempDir(), fakeCLIOptions(t, "hang")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = a.Close() }()

	b, err := New(t.TempDir(), fakeCLIOptions(t, "hang")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = b.Close() }()

	if a.PID() == b.PID() || a.PID() <= 0 || b.PID() <= 0 {
		t.Fatalf("each client has its own CLI process: %d and %d", a.PID(), b.PID())
	}
}

func TestClient_PIDOfANilClientIsZero(t *testing.T) {
	t.Parallel()

	var c *Client
	if got := c.PID(); got != 0 {
		t.Fatalf("(*Client)(nil).PID() = %d, want 0", got)
	}

	if got := (&Client{}).PID(); got != 0 {
		t.Fatalf("a client with no transport: PID() = %d, want 0", got)
	}
}
