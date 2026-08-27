package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
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

	// ackInitialize consumes (and answers) the initialize control_request
	// the client sends in New(), so every scenario starts with the control
	// channel established.
	// controlResponseLine is shared with the test-side capture reader.
	controlResponseLine := func(requestID string, response any) map[string]any {
		return map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": requestID,
				"response":   response,
			},
		}
	}

	ackInitialize := func() {
		if !stdin.Scan() {
			panic("fake cli: stdin closed before initialize")
		}
		var env struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "control_request" {
			panic(fmt.Sprintf("fake cli: expected initialize control_request, got %q", stdin.Text()))
		}
		writeLine(controlResponseLine(env.RequestID, map[string]any{"success": true}))
	}

	// sendControlRequestToClient writes an inbound control_request and (for
	// scenarios that need it) waits for the client's control_response.
	sendControlRequestToClient := func(requestID string, request map[string]any) {
		writeLine(map[string]any{
			"type":       "control_request",
			"request_id": requestID,
			"request":    request,
		})
	}

	// readControlResponse scans stdin until it reads a control_response,
	// returning its raw payload map.
	readControlResponse := func() map[string]any {
		for stdin.Scan() {
			var env struct {
				Type     string `json:"type"`
				Response struct {
					Subtype   string         `json:"subtype"`
					RequestID string         `json:"request_id"`
					Response  map[string]any `json:"response"`
					Error     string         `json:"error"`
				} `json:"response"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err != nil {
				continue
			}
			if env.Type == "control_response" {
				return map[string]any{
					"subtype":    env.Response.Subtype,
					"request_id": env.Response.RequestID,
					"response":   env.Response.Response,
					"error":      env.Response.Error,
				}
			}
		}
		panic("fake cli: stdin closed waiting for control_response")
	}

	switch scenario {
	case "streaming_and_permission":
		ackInitialize()
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
		ackInitialize()
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
		ackInitialize()
		stdin.Scan() // consume the prompt line, then never respond or exit
		// Not select{}: with nothing else running, that's a single goroutine
		// blocked with no way to ever wake up, which Go's runtime deadlock
		// detector kills immediately (fatal error, exit status 2) -- exactly
		// the false-positive "already dead" this scenario must not produce.
		// A pending timer keeps the runtime from considering it deadlocked.
		time.Sleep(time.Hour)

	case "capture":
		ackInitialize()
		// Reports this process's argv and environment back through the
		// result message so flag/env-construction tests can assert exactly
		// what the client spawned us with. Consume the prompt line first
		// so the client's stdin write always completes before we exit.
		stdin.Scan()
		dump, err := json.Marshal(map[string]any{
			"args": os.Args[1:],
			"env":  os.Environ(),
		})
		if err != nil {
			panic(err)
		}
		writeLine(map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-capture",
			"result":     string(dump),
		})

	case "capture_stdin":
		// Records every stdin line verbatim and acks each control_request
		// with an empty success response. Once CLAUDECODE_FAKE_MAX_LINES
		// control requests have been seen, reports the recorded lines via
		// the result message (prompted by a final user turn if one was
		// sent) so tests can assert the exact JSON the client sent.
		ackInitialize()
		maxReqs, _ := strconv.Atoi(os.Getenv("CLAUDECODE_FAKE_MAX_LINES"))
		var lines []string
		seen := 0
		for seen < maxReqs && stdin.Scan() {
			lines = append(lines, stdin.Text())
			var env struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err == nil && env.Type == "control_request" {
				writeLine(controlResponseLine(env.RequestID, map[string]any{}))
				seen++
			}
		}
		dump, err := json.Marshal(lines)
		if err != nil {
			panic(err)
		}
		writeLine(map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-capture",
			"result":     string(dump),
		})

	case "control_echo":
		// A control_response for an unknown request_id must be silently
		// ignored by the client -- send one up front.
		writeLine(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": "req-never-existed",
				"response":   map[string]any{},
			},
		})
		// Answers every outbound control_request with a canned response read
		// from the CLAUDECODE_FAKE_RESPONSES env var (a JSON object keyed by
		// subtype), then exits once stdin closes or after the prompt line
		// is consumed and a final result is sent. Used for outbound-method
		// happy-path/error/timeout tests.
		ackInitialize()
		var canned map[string]json.RawMessage
		if err := json.Unmarshal([]byte(os.Getenv("CLAUDECODE_FAKE_RESPONSES")), &canned); err != nil {
			panic(err)
		}
		// Read stdin until EOF, answering each control_request (except the
		// subtypes named in CLAUDECODE_FAKE_IGNORE, comma-separated, which
		// get no response at all -- for timeout tests).
		ignore := map[string]bool{}
		for _, s := range strings.Split(os.Getenv("CLAUDECODE_FAKE_IGNORE"), ",") {
			if s != "" {
				ignore[s] = true
			}
		}
		for stdin.Scan() {
			var env struct {
				Type      string          `json:"type"`
				RequestID string          `json:"request_id"`
				Request   json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "control_request" {
				continue
			}
			var inner struct {
				Subtype string `json:"subtype"`
			}
			_ = json.Unmarshal(env.Request, &inner)
			if ignore[inner.Subtype] {
				continue
			}
			raw, ok := canned[inner.Subtype]
			if !ok {
				raw, _ = json.Marshal(map[string]any{})
			}
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				panic(err)
			}
			if e, isErr := payload["__error"].(string); isErr {
				delete(payload, "__error")
				writeLine(map[string]any{
					"type": "control_response",
					"response": map[string]any{
						"subtype":    "error",
						"request_id": env.RequestID,
						"error":      e,
					},
				})
				continue
			}
			writeLine(map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype":    "success",
					"request_id": env.RequestID,
					"response":   payload,
				},
			})
		}

	case "control_traffic":
		// Scripted inbound control traffic after the prompt line: sends an
		// mcp_message control_request (unsupported-subtype pin), a
		// hook_callback for an unregistered ID, a malformed control_request
		// line, then finishes the turn. Asserts the client's error
		// responses by echoing them into the result message.
		ackInitialize()
		stdin.Scan() // consume the prompt line

		sendControlRequestToClient("req-mcp", map[string]any{"subtype": "mcp_message"})
		mcpResp := readControlResponse()

		sendControlRequestToClient("req-hook", map[string]any{
			"subtype":     "hook_callback",
			"callback_id": "nope",
			"input":       map[string]any{},
			"tool_use_id": nil,
		})
		hookResp := readControlResponse()

		writeLine(map[string]any{
			"type":       "control_request",
			"request_id": "req-bad",
			"request":    "not an object",
		})
		// No well-formed control_request => client sends no response; just
		// proceed. The malformed envelope with a missing request_id:
		fmt.Fprintln(os.Stdout, `{"type":"control_request","request":{"subtype":"can_use_tool"}}`)

		// A can_use_tool with the enriched fields, to prove they reach the
		// policy.
		sendControlRequestToClient("req-cut", map[string]any{
			"subtype":         "can_use_tool",
			"tool_name":       "Bash",
			"input":           map[string]any{"command": "ls"},
			"tool_use_id":     "tool-9",
			"title":           "t",
			"display_name":    "dn",
			"description":     "d",
			"decision_reason": "dr",
			"blocked_path":    "/tmp/x",
			"agent_id":        "agent-1",
		})
		cutResp := readControlResponse()

		dump, err := json.Marshal(map[string]any{
			"mcp":  mcpResp,
			"hook": hookResp,
			"cut":  cutResp,
		})
		if err != nil {
			panic(err)
		}
		writeLine(map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-1",
			"result":     string(dump),
		})

	case "hook_dispatch":
		// Sends a hook_callback for an ID the test registered directly into
		// the client's callback map (via env-var-passed ID read back by the
		// test from stderr), plus a control_cancel_request for a
		// long-running handler.
		ackInitialize()
		stdin.Scan() // consume the prompt line

		hookID := os.Getenv("CLAUDECODE_FAKE_HOOK_ID")
		sendControlRequestToClient("req-h1", map[string]any{
			"subtype":     "hook_callback",
			"callback_id": hookID,
			"input":       map[string]any{"a": 1},
			"tool_use_id": "tool-h",
		})
		h1 := readControlResponse()

		// cancel test: a can_use_tool whose policy blocks until cancelled.
		sendControlRequestToClient("req-h2", map[string]any{
			"subtype":     "can_use_tool",
			"tool_name":   "Bash",
			"input":       map[string]any{"command": "sleep"},
			"tool_use_id": "tool-c",
		})
		writeLine(map[string]any{
			"type":       "control_cancel_request",
			"request_id": "req-h2",
		})

		// If the cancel was honored, no control_response is ever written for
		// req-h2; the next response the client sends must be for this marker
		// request and no other.
		sendControlRequestToClient("req-h3", map[string]any{
			"subtype":     "hook_callback",
			"callback_id": hookID,
			"input":       map[string]any{"a": 2},
			"tool_use_id": "tool-h3",
		})
		h3 := readControlResponse()

		dump, err := json.Marshal(map[string]any{"hook": h1, "after_cancel": h3})
		if err != nil {
			panic(err)
		}
		writeLine(map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-1",
			"result":     string(dump),
		})

	case "hooks_roundtrip":
		// Captures the initialize request's hooks payload, then sends a
		// PreToolUse-shaped hook_callback for hook_0 and dumps both the
		// payload and the client's control_response into the result.
		if !stdin.Scan() {
			panic("fake cli: stdin closed before initialize")
		}
		var initEnv struct {
			RequestID string `json:"request_id"`
			Request   struct {
				Hooks map[string][]map[string]any `json:"hooks"`
			} `json:"request"`
		}
		if err := json.Unmarshal(stdin.Bytes(), &initEnv); err != nil {
			panic(err)
		}
		writeLine(controlResponseLine(initEnv.RequestID, map[string]any{"success": true}))
		stdin.Scan() // consume the prompt line

		sendControlRequestToClient("req-hk", map[string]any{
			"subtype":     "hook_callback",
			"callback_id": "hook_0",
			"input": map[string]any{
				"session_id":      "sess-1",
				"transcript_path": "/tmp/t.jsonl",
				"cwd":             "/tmp",
				"hook_event_name": "PreToolUse",
				"tool_name":       "Bash",
				"tool_input":      map[string]any{"command": "ls"},
				"tool_use_id":     "tool-1",
			},
			"tool_use_id": "tool-1",
		})
		hookResp := readControlResponse()

		dump, err := json.Marshal(map[string]any{
			"initialize_hooks": initEnv.Request.Hooks,
			"hook":             hookResp,
		})
		if err != nil {
			panic(err)
		}
		writeLine(map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-1",
			"result":     string(dump),
		})

	case "hooks_concurrent":
		// Sends two hook_callback requests back-to-back without waiting
		// for either response -- the client must dispatch them
		// concurrently or the callbacks (which block until both are
		// entered) deadlock this scenario.
		ackInitialize()
		stdin.Scan() // consume the prompt line
		sendControlRequestToClient("req-hc1", map[string]any{
			"subtype":     "hook_callback",
			"callback_id": "hook_0",
			"input":       map[string]any{"hook_event_name": "PostToolUse"},
			"tool_use_id": "tool-a",
		})
		sendControlRequestToClient("req-hc2", map[string]any{
			"subtype":     "hook_callback",
			"callback_id": "hook_1",
			"input":       map[string]any{"hook_event_name": "PostToolUse"},
			"tool_use_id": "tool-b",
		})
		r1 := readControlResponse()
		r2 := readControlResponse()
		dump, err := json.Marshal(map[string]any{"responses": []map[string]any{r1, r2}})
		if err != nil {
			panic(err)
		}
		writeLine(map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-1",
			"result":     string(dump),
		})

	case "two_turns":
		// Two full turn cycles on one subprocess: proves sequential Prompt
		// calls reuse the same CLI process.
		ackInitialize()
		for turn := 1; turn <= 2; turn++ {
			stdin.Scan() // consume the prompt line
			writeLine(map[string]any{
				"type":       "assistant",
				"session_id": "sess-1",
				"message": map[string]any{
					"model":   "claude-fake",
					"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("turn %d", turn)}},
				},
			})
			writeLine(map[string]any{
				"type":       "result",
				"subtype":    "success",
				"is_error":   false,
				"num_turns":  1,
				"session_id": "sess-1",
				"result":     fmt.Sprintf("done-%d", turn),
			})
		}

	case "interruptible":
		// Streams one assistant message, then waits for the client's
		// interrupt control_request, acks it, and finishes the turn.
		ackInitialize()
		stdin.Scan() // consume the prompt line
		writeLine(map[string]any{
			"type":       "assistant",
			"session_id": "sess-1",
			"message": map[string]any{
				"model":   "claude-fake",
				"content": []any{map[string]any{"type": "text", "text": "working"}},
			},
		})
		// Wait for the interrupt request (skipping anything else).
		for stdin.Scan() {
			var env struct {
				Type      string          `json:"type"`
				RequestID string          `json:"request_id"`
				Request   json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "control_request" {
				continue
			}
			var inner struct {
				Subtype string `json:"subtype"`
			}
			_ = json.Unmarshal(env.Request, &inner)
			if inner.Subtype == "interrupt" {
				writeLine(controlResponseLine(env.RequestID, map[string]any{}))
				break
			}
		}
		writeLine(map[string]any{
			"type":        "result",
			"subtype":     "success",
			"is_error":    false,
			"num_turns":   1,
			"session_id":  "sess-1",
			"result":      "interrupted",
			"stop_reason": "interrupted",
		})

	case "reorder":
		// Reads two control requests, then answers the second one first --
		// correlation must survive out-of-order responses.
		ackInitialize()
		var ids []string
		for len(ids) < 2 && stdin.Scan() {
			var env struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "control_request" {
				continue
			}
			ids = append(ids, env.RequestID)
		}
		if len(ids) == 2 {
			writeLine(controlResponseLine(ids[1], map[string]any{}))
			writeLine(controlResponseLine(ids[0], map[string]any{}))
		}
		// Keep the process alive until stdin closes so late responses still
		// have a reader.
		for stdin.Scan() {
		}

	case "policy_roundtrip":
		// Three can_use_tool round trips driven by the test's scriptable
		// policy; responses are dumped into the result for assertion.
		ackInitialize()
		stdin.Scan() // consume the prompt line
		var resps []map[string]any
		for _, id := range []string{"req-a", "req-b", "req-c"} {
			sendControlRequestToClient(id, map[string]any{
				"subtype":         "can_use_tool",
				"tool_name":       "Bash",
				"input":           map[string]any{"command": "echo"},
				"tool_use_id":     id,
				"title":           "t",
				"display_name":    "dn",
				"description":     "d",
				"decision_reason": "dr",
				"blocked_path":    "/tmp/x",
				"agent_id":        "agent-1",
			})
			resps = append(resps, readControlResponse())
		}
		dump, err := json.Marshal(resps)
		if err != nil {
			panic(err)
		}
		writeLine(map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-1",
			"result":     string(dump),
		})

	case "crash":
		// Acknowledge initialize, consume the prompt, then exit with stdout
		// closed mid-turn -- simulates a CLI crash. Any pending outbound
		// control request must force-resolve with an error.
		ackInitialize()
		stdin.Scan()
		os.Exit(0)

	default:
		fmt.Fprintf(os.Stderr, "fake cli: unknown scenario %q\n", scenario)
		os.Exit(1)
	}

	os.Exit(0)
}
