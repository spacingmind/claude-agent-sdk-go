package claudecode

import (
	"context"
	"testing"
	"time"
)

type promptResult struct {
	res ResultMessage
	err error
}

func TestClient_FullTurnStreamingAndPermission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		policy       PermissionPolicy
		wantBehavior string
	}{
		{name: "default policy denies", policy: nil, wantBehavior: "deny"},
		{name: "auto-approve policy allows", policy: AutoApprovePolicy{}, wantBehavior: "allow"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := fakeCLIOptions(t, "streaming_and_permission")
			if tt.policy != nil {
				opts = append(opts, WithPermissionPolicy(tt.policy))
			}
			c, err := New(t.TempDir(), opts...)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer c.Close()

			updates := make(chan Message)
			resultCh := make(chan promptResult, 1)
			go func() {
				res, err := c.Prompt(context.Background(), "do the thing", updates)
				resultCh <- promptResult{res, err}
			}()

			wantText := func(msg Message, want string) {
				t.Helper()
				am, ok := msg.(AssistantMessage)
				if !ok || len(am.Content) != 1 {
					t.Fatalf("update = %#v, want single-block AssistantMessage", msg)
				}
				tb, ok := am.Content[0].(TextBlock)
				if !ok || tb.Text != want {
					t.Fatalf("update text = %#v, want %q", am.Content[0], want)
				}
			}

			select {
			case msg, ok := <-updates:
				if !ok {
					t.Fatal("updates closed before first message")
				}
				wantText(msg, "step one")
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for first streamed message")
			}

			// By the time this second message can possibly arrive, the
			// control_request/control_response round trip has already
			// completed (see runFakeCLI's "streaming_and_permission" case
			// for why that's a structural guarantee, not a timing guess).
			select {
			case msg, ok := <-updates:
				if !ok {
					t.Fatal("updates closed before second message")
				}
				wantText(msg, "control behavior: "+tt.wantBehavior)
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for second streamed message (permission round trip)")
			}

			select {
			case _, ok := <-updates:
				if ok {
					t.Fatal("updates received a third message, want channel closed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for updates to close")
			}

			select {
			case r := <-resultCh:
				if r.err != nil {
					t.Fatalf("Prompt() error = %v", r.err)
				}
				want := "final:" + tt.wantBehavior
				if r.res.Result != want {
					t.Fatalf("Prompt() result = %q, want %q", r.res.Result, want)
				}
				if r.res.SessionID != "sess-1" {
					t.Fatalf("Prompt() session id = %q, want %q", r.res.SessionID, "sess-1")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for Prompt() to return")
			}
		})
	}
}

func TestClient_MalformedMessagesSkippedGracefully(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "malformed")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer c.Close()

	updates := make(chan Message)
	var got []Message
	drainDone := make(chan struct{})
	go func() {
		for msg := range updates {
			got = append(got, msg)
		}
		close(drainDone)
	}()

	resultCh := make(chan promptResult, 1)
	go func() {
		res, err := c.Prompt(context.Background(), "hi", updates)
		resultCh <- promptResult{res, err}
	}()

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("Prompt() error = %v, want nil (malformed lines should be skipped)", r.err)
		}
		if r.res.Result != "done" {
			t.Fatalf("Prompt() result = %q, want %q", r.res.Result, "done")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Prompt() to return")
	}

	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for updates to be drained")
	}

	if len(got) != 1 {
		t.Fatalf("got %d updates, want 1: %#v", len(got), got)
	}
	am, ok := got[0].(AssistantMessage)
	if !ok || len(am.Content) != 1 {
		t.Fatalf("update = %#v, want single-block AssistantMessage", got[0])
	}
	tb, ok := am.Content[0].(TextBlock)
	if !ok || tb.Text != "ok" {
		t.Fatalf("update text = %#v, want %q", am.Content[0], "ok")
	}
}

func TestClient_CloseForceKillsHungProcess(t *testing.T) {
	t.Parallel()

	opts := append(fakeCLIOptions(t, "hang"), withCloseGracePeriod(200*time.Millisecond))
	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updates := make(chan Message)
	promptDone := make(chan struct{})
	go func() {
		_, _ = c.Prompt(context.Background(), "hi", updates)
		close(promptDone)
	}()
	go func() {
		for range updates {
		}
	}()

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if c.tr.cmd.ProcessState == nil {
		t.Fatal("Close() returned but the subprocess was not reaped")
	}

	select {
	case <-promptDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt() did not return after Close() killed the subprocess")
	}
}

func TestClient_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	opts := append(fakeCLIOptions(t, "hang"), withCloseGracePeriod(200*time.Millisecond))
	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updates := make(chan Message)
	go func() { _, _ = c.Prompt(context.Background(), "hi", updates) }()
	go func() {
		for range updates {
		}
	}()

	if err := c.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
