package claudecode

import (
	"context"
	"testing"
	"time"
)

// promptCollect runs one Prompt against the fake CLI scenario and returns the
// result and every message forwarded onto updates.
func promptCollect(t *testing.T, scenario string) (ResultMessage, []Message) {
	t.Helper()

	c, err := New(t.TempDir(), fakeCLIOptions(t, scenario)...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)
	got := make(chan []Message, 1)

	go func() {
		var msgs []Message
		for msg := range updates {
			msgs = append(msgs, msg)
		}

		got <- msgs
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := c.Prompt(ctx, "ping", updates)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	return res, <-got
}

func TestPrompt_SkipsBackgroundTaskNotificationResult(t *testing.T) {
	t.Parallel()

	res, msgs := promptCollect(t, "stale_background_result")

	if res.Result != "pong" || res.NumTurns != 1 || res.StopReason != "end_turn" {
		t.Fatalf("Prompt() result = %+v, want the turn's own result (pong, 1 turn, end_turn)", res)
	}

	var sawNotification, sawAssistant bool

	for _, m := range msgs {
		switch m.(type) {
		case TaskNotificationMessage:
			sawNotification = true
		case AssistantMessage:
			sawAssistant = true
		case ResultMessage:
			t.Fatalf("updates carried a ResultMessage %+v; results are returned, never forwarded", m)
		}
	}

	if !sawNotification || !sawAssistant {
		t.Fatalf("updates = %#v, want the task_notification and the assistant reply forwarded", msgs)
	}
}

func TestPrompt_EmptyResultWithoutNotificationEndsTurn(t *testing.T) {
	t.Parallel()

	res, _ := promptCollect(t, "empty_result_no_notification")

	if res.NumTurns != 0 || res.Result != "" {
		t.Fatalf("Prompt() result = %+v, want the empty result itself", res)
	}
}

func TestIsNotificationResult(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		res  ResultMessage
		want bool
	}{
		{"empty zero-turn", ResultMessage{}, true},
		{"usage repeated from previous turn", ResultMessage{ModelUsage: map[string]ModelUsage{"m": {}}}, true},
		{"real turn", ResultMessage{NumTurns: 1, StopReason: "end_turn", Result: "ok"}, false},
		{"error", ResultMessage{IsError: true}, false},
		{"has output", ResultMessage{Result: "x"}, false},
		{"has stop reason", ResultMessage{StopReason: "end_turn"}, false},
	}

	for _, tc := range cases {
		if got := isNotificationResult(tc.res); got != tc.want {
			t.Errorf("%s: isNotificationResult() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
