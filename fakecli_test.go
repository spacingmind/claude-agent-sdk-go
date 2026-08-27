package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestMain re-execs the test binary itself as the fake `claude` CLI when
// CLAUDECODE_FAKE_CLI is set, following the standard Go "helper process"
// pattern (as in os/exec's own tests). This avoids depending on the real
// claude binary being installed/authenticated, which CI can't guarantee.
func TestMain(m *testing.M) {
	if os.Getenv("CLAUDECODE_FAKE_CLI") == "1" {
		runFakeCLI()
		return
	}
	os.Exit(m.Run())
}

// fakeCLIOptions points a Client at this test binary running as the fake
// CLI for the given scenario (see runFakeCLI).
func fakeCLIOptions(t *testing.T, scenario string) []Option {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	env := append(os.Environ(),
		"CLAUDECODE_FAKE_CLI=1",
		"CLAUDECODE_FAKE_SCENARIO="+scenario,
	)
	return []Option{
		WithCLIPath(self),
		withExtraEnv(env),
	}
}

func runFakeCLI() {
	scenario := os.Getenv("CLAUDECODE_FAKE_SCENARIO")

	stdin := bufio.NewScanner(os.Stdin)
	stdin.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	writeLine := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(os.Stdout, "%s\n", data)
	}

	switch scenario {
	case "streaming_and_permission":
		// Proves both true incremental streaming and the control-request
		// round trip via blocking causality, not a timing guess: this
		// process will not write the second assistant message until it has
		// read a control_response on stdin, and the client can only have
		// sent that control_response after its single-goroutine read loop
		// already dispatched the first assistant message onto the updates
		// channel (message handling is strictly sequential per line). So a
		// test observing message 1 before message 2 is a structural
		// guarantee, not a race -- see fix-flaky-streaming-test for why a
		// fixed-timeout "hasn't arrived yet" check was rejected here.
		stdin.Scan() // consume the prompt line

		writeLine(map[string]any{
			"type":       "assistant",
			"session_id": "sess-1",
			"message": map[string]any{
				"model": "claude-fake",
				"content": []any{
					map[string]any{"type": "text", "text": "step one"},
				},
			},
		})

		writeLine(map[string]any{
			"type":       "control_request",
			"request_id": "req-1",
			"request": map[string]any{
				"subtype":     "can_use_tool",
				"tool_name":   "Bash",
				"input":       map[string]any{"command": "echo hi"},
				"tool_use_id": "tool-1",
			},
		})

		stdin.Scan()
		var resp struct {
			Response struct {
				Response struct {
					Behavior string `json:"behavior"`
				} `json:"response"`
			} `json:"response"`
		}
		_ = json.Unmarshal(stdin.Bytes(), &resp)
		behavior := resp.Response.Response.Behavior
		if behavior == "" {
			behavior = "unknown"
		}

		writeLine(map[string]any{
			"type":       "assistant",
			"session_id": "sess-1",
			"message": map[string]any{
				"model": "claude-fake",
				"content": []any{
					map[string]any{"type": "text", "text": "control behavior: " + behavior},
				},
			},
		})

		writeLine(map[string]any{
			"type":            "result",
			"subtype":         "success",
			"duration_ms":     1,
			"duration_api_ms": 1,
			"is_error":        false,
			"num_turns":       1,
			"session_id":      "sess-1",
			"result":          "final:" + behavior,
		})

	case "malformed":
		stdin.Scan() // consume the prompt line

		fmt.Fprintln(os.Stdout, "not json at all")
		writeLine(map[string]any{"type": "unknown_type", "foo": "bar"})
		fmt.Fprintln(os.Stdout, "")

		writeLine(map[string]any{
			"type":       "assistant",
			"session_id": "sess-1",
			"message": map[string]any{
				"model":   "claude-fake",
				"content": []any{map[string]any{"type": "text", "text": "ok"}},
			},
		})

		writeLine(map[string]any{
			"type":       "result",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-1",
			"result":     "done",
		})

	case "hang":
		stdin.Scan() // consume the prompt line, then never respond or exit
		// Not select{}: with nothing else running, that's a single goroutine
		// blocked with no way to ever wake up, which Go's runtime deadlock
		// detector kills immediately (fatal error, exit status 2) -- exactly
		// the false-positive "already dead" this scenario must not produce.
		// A pending timer keeps the runtime from considering it deadlocked.
		time.Sleep(time.Hour)

	default:
		fmt.Fprintf(os.Stderr, "fake cli: unknown scenario %q\n", scenario)
		os.Exit(1)
	}

	os.Exit(0)
}
