package claudecode

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// burstMessages is how many messages the burst_* fake-CLI scenarios write
// before dying; at ~0.5 KiB each it exceeds a pipe buffer, so unread bytes
// are still queued in the pipe when the process exits.
const burstMessages = 200

// runBurst runs a burst_* scenario to stream end and returns how many
// assistant messages were delivered plus the client (for Err/ExitStatus).
func runBurst(t *testing.T, scenario string) (int, *Client) {
	t.Helper()

	c, err := New(t.TempDir(), fakeCLIOptions(t, scenario)...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	n := 0

	for m := range c.ReceiveMessages(ctx) {
		if _, ok := m.(AssistantMessage); ok {
			n++
		}
	}

	return n, c
}

func TestExitStatus_BurstThenExitDeliversAllAndExitCode(t *testing.T) {
	t.Parallel()

	n, c := runBurst(t, "burst_exit")
	if n != burstMessages {
		t.Fatalf("delivered %d messages, want %d", n, burstMessages)
	}

	pe, ok := errors.AsType[*ProcessError](c.Err())
	if !ok {
		t.Fatalf("Err() = %T (%v), want *ProcessError", c.Err(), c.Err())
	}

	if pe.ExitCode != 3 || pe.Signal != 0 {
		t.Fatalf("ProcessError = code %d signal %v, want code 3 signal 0", pe.ExitCode, pe.Signal)
	}

	st, ok := c.ExitStatus()
	if !ok || st.Code != 3 {
		t.Fatalf("ExitStatus() = %+v, %v; want code 3, ok", st, ok)
	}
}

func TestExitStatus_BurstThenSignalReportsSignal(t *testing.T) {
	t.Parallel()

	n, c := runBurst(t, "burst_signal")
	if n != burstMessages {
		t.Fatalf("delivered %d messages, want %d", n, burstMessages)
	}

	pe, ok := errors.AsType[*ProcessError](c.Err())
	if !ok {
		t.Fatalf("Err() = %T (%v), want *ProcessError", c.Err(), c.Err())
	}

	if pe.Signal != syscall.SIGKILL {
		t.Fatalf("ProcessError.Signal = %v, want SIGKILL", pe.Signal)
	}

	if st, ok := c.ExitStatus(); !ok || st.Signal != syscall.SIGKILL {
		t.Fatalf("ExitStatus() = %+v, %v; want SIGKILL", st, ok)
	}
}

func TestExitStatus_StderrDrainedBeforeExitError(t *testing.T) {
	t.Parallel()

	var logs strings.Builder

	opts := append(fakeCLIOptions(t, "burst_stderr_exit"), WithLogWriter(&syncWriter{w: &logs}))

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	for range c.ReceiveMessages(context.Background()) { // drain until the stream closes
	}

	pe, ok := errors.AsType[*ProcessError](c.Err())
	if !ok {
		t.Fatalf("Err() = %T (%v), want *ProcessError", c.Err(), c.Err())
	}

	if pe.ExitCode != 4 || !strings.Contains(pe.Stderr, "fatal: something broke") {
		t.Fatalf("ProcessError = %+v, want code 4 with stderr tail", pe)
	}
}

type syncWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.w.Write(p)
}

func TestExitStatus_ResultErrorStillExposesExitStatus(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "crash_result_error")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	go func() { _, _ = c.Prompt(context.Background(), "hi", make(chan Message, 100)) }()

	for range c.ReceiveMessages(context.Background()) { // drain until the stream closes
	}

	if _, ok := errors.AsType[*ResultError](c.Err()); !ok {
		t.Fatalf("Err() = %T, want *ResultError", c.Err())
	}

	if st, ok := c.ExitStatus(); !ok || st.Code != 0 {
		t.Fatalf("ExitStatus() = %+v, %v; want code 0, ok", st, ok)
	}
}

// TestExitStatus_LoadNoLoss runs the burst scenario many times under CPU
// burner goroutines and requires zero lost messages and a correct exit
// code every time. Burners stop (and are joined) before the test returns.
func TestExitStatus_LoadNoLoss(t *testing.T) {
	iterations := 200
	if testing.Short() {
		iterations = 20
	}

	stop := make(chan struct{})

	var burners sync.WaitGroup

	for range 2 * runtime.NumCPU() {
		burners.Add(1)

		go func() {
			defer burners.Done()

			x := 0
			for {
				select {
				case <-stop:
					return
				default:
				}

				for range 100000 {
					x++
				}
			}
		}()
	}

	defer func() {
		close(stop)
		burners.Wait()
	}()

	sem := make(chan struct{}, 8)

	var wg sync.WaitGroup

	for i := range iterations {
		wg.Add(1)

		sem <- struct{}{}

		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			n, c := runBurst(t, "burst_exit")
			if n != burstMessages {
				t.Errorf("iteration %d: delivered %d/%d messages", i, n, burstMessages)
			}

			if pe, ok := errors.AsType[*ProcessError](c.Err()); !ok || pe.ExitCode != 3 {
				t.Errorf("iteration %d: Err() = %v, want ProcessError code 3", i, c.Err())
			}
		}()
	}

	wg.Wait()
}
