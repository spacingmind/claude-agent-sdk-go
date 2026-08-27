package claudecode

import (
	"encoding/json"
	"reflect"
	"testing"
)

// decodeLineTestCase is shared by the table-driven tests below: feed one
// NDJSON line to decodeLine and compare the decoded message with want.
type decodeLineTestCase struct {
	name string
	line string
	want any
}

func runDecodeLineTests(t *testing.T, tests []decodeLineTestCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeLine([]byte(tt.line))
			if err != nil {
				t.Fatalf("decodeLine() error = %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decodeLine() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDecodeLine_ContentBlocks(t *testing.T) {
	runDecodeLineTests(t, []decodeLineTestCase{
		{
			name: "thinking block on assistant message",
			line: `{"type":"assistant","session_id":"s1","message":{"model":"m","content":[{"type":"thinking","thinking":"hm","signature":"sig-1"}]}}`,
			want: AssistantMessage{
				Content:   []ContentBlock{ThinkingBlock{Thinking: "hm", Signature: "sig-1"}},
				Model:     "m",
				SessionID: "s1",
			},
		},
		{
			name: "tool_result block on assistant message with string content",
			line: `{"type":"assistant","session_id":"s1","message":{"model":"m","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"boom","is_error":true}]}}`,
			want: AssistantMessage{
				Content: []ContentBlock{ToolResultBlock{
					ToolUseID: "tu-1",
					Content:   json.RawMessage(`"boom"`),
					IsError:   true,
				}},
				Model:     "m",
				SessionID: "s1",
			},
		},
		{
			name: "tool_result block on user message with array content",
			line: `{"type":"user","session_id":"s1","message":{"content":[{"type":"tool_result","tool_use_id":"tu-2","content":[{"type":"text","text":"out"}]}]}}`,
			want: UserMessage{
				Content: []ContentBlock{ToolResultBlock{
					ToolUseID: "tu-2",
					Content:   json.RawMessage(`[{"type":"text","text":"out"}]`),
				}},
				SessionID: "s1",
			},
		},
		{
			name: "tool_result block with absent content and is_error",
			line: `{"type":"user","session_id":"s1","message":{"content":[{"type":"tool_result","tool_use_id":"tu-3"}]}}`,
			want: UserMessage{
				Content:   []ContentBlock{ToolResultBlock{ToolUseID: "tu-3"}},
				SessionID: "s1",
			},
		},
		{
			name: "server_tool_use block",
			line: `{"type":"assistant","session_id":"s1","message":{"model":"m","content":[{"type":"server_tool_use","id":"stu-1","name":"web_search","input":{"q":"go"}}]}}`,
			want: AssistantMessage{
				Content: []ContentBlock{ServerToolUseBlock{
					ID:    "stu-1",
					Name:  "web_search",
					Input: map[string]any{"q": "go"},
				}},
				Model:     "m",
				SessionID: "s1",
			},
		},
		{
			name: "advisor_tool_result block decodes to ServerToolResultBlock",
			line: `{"type":"assistant","session_id":"s1","message":{"model":"m","content":[{"type":"advisor_tool_result","tool_use_id":"stu-1","content":{"found":2}}]}}`,
			want: AssistantMessage{
				Content: []ContentBlock{ServerToolResultBlock{
					ToolUseID: "stu-1",
					Content:   json.RawMessage(`{"found":2}`),
				}},
				Model:     "m",
				SessionID: "s1",
			},
		},
		{
			name: "mixed known and unknown block types",
			line: `{"type":"assistant","session_id":"s1","message":{"model":"m","content":[{"type":"text","text":"a"},{"type":"mystery","x":1},{"type":"thinking","thinking":"b","signature":"s"}]}}`,
			want: AssistantMessage{
				Content: []ContentBlock{
					TextBlock{Text: "a"},
					RawBlock{Type: "mystery", Raw: json.RawMessage(`{"type":"mystery","x":1}`)},
					ThinkingBlock{Thinking: "b", Signature: "s"},
				},
				Model:     "m",
				SessionID: "s1",
			},
		},
	})
}

func TestDecodeLine_TopLevelMessages(t *testing.T) {
	runDecodeLineTests(t, []decodeLineTestCase{
		{
			name: "stream_event",
			line: `{"type":"stream_event","uuid":"u1","session_id":"s1","event":{"type":"content_block_delta","delta":{"text":"hi"}},"parent_tool_use_id":"ptu"}`,
			want: StreamEvent{
				UUID:            "u1",
				SessionID:       "s1",
				Event:           json.RawMessage(`{"type":"content_block_delta","delta":{"text":"hi"}}`),
				ParentToolUseID: "ptu",
			},
		},
		{
			name: "stream_event without parent_tool_use_id",
			line: `{"type":"stream_event","uuid":"u1","session_id":"s1","event":{"type":"message_start"}}`,
			want: StreamEvent{
				UUID:      "u1",
				SessionID: "s1",
				Event:     json.RawMessage(`{"type":"message_start"}`),
			},
		},
		{
			name: "rate_limit_event with all fields",
			line: `{"type":"rate_limit_event","uuid":"u1","session_id":"s1","rate_limit_info":{"status":"warning","resetsAt":1234,"rateLimitType":"requests","utilization":0.75,"overageStatus":"active","overageResetsAt":5678,"overageDisabledReason":"org"}}`,
			want: RateLimitEvent{
				RateLimitInfo: RateLimitInfo{
					Status:                "warning",
					ResetsAt:              1234,
					RateLimitType:         "requests",
					Utilization:           0.75,
					OverageStatus:         "active",
					OverageResetsAt:       5678,
					OverageDisabledReason: "org",
				},
				UUID:      "u1",
				SessionID: "s1",
			},
		},
		{
			name: "rate_limit_event with only required fields",
			line: `{"type":"rate_limit_event","uuid":"u1","session_id":"s1","rate_limit_info":{"status":"ok"}}`,
			want: RateLimitEvent{
				RateLimitInfo: RateLimitInfo{Status: "ok"},
				UUID:          "u1",
				SessionID:     "s1",
			},
		},
		{
			name: "conversation_reset",
			line: `{"type":"conversation_reset","new_conversation_id":"c2","uuid":"u1","session_id":"s1"}`,
			want: ConversationResetMessage{
				NewConversationID: "c2",
				UUID:              "u1",
				SessionID:         "s1",
			},
		},
		{
			name: "unknown top-level type still skipped",
			line: `{"type":"something_new","x":1}`,
			want: nil,
		},
	})
}

func TestDecodeLine_SystemSubtypes(t *testing.T) {
	runDecodeLineTests(t, []decodeLineTestCase{
		{
			name: "task_started",
			line: `{"type":"system","subtype":"task_started","task_id":"t1","description":"d","uuid":"u1","session_id":"s1","tool_use_id":"tu","task_type":"agent"}`,
			want: TaskStartedMessage{
				TaskID:      "t1",
				Description: "d",
				UUID:        "u1",
				SessionID:   "s1",
				ToolUseID:   "tu",
				TaskType:    "agent",
			},
		},
		{
			name: "task_started without optional fields",
			line: `{"type":"system","subtype":"task_started","task_id":"t1","description":"d","uuid":"u1","session_id":"s1"}`,
			want: TaskStartedMessage{
				TaskID:      "t1",
				Description: "d",
				UUID:        "u1",
				SessionID:   "s1",
			},
		},
		{
			name: "task_progress",
			line: `{"type":"system","subtype":"task_progress","task_id":"t1","description":"d","usage":{"total_tokens":10,"tool_uses":2,"duration_ms":3000},"uuid":"u1","session_id":"s1","tool_use_id":"tu","last_tool_name":"Bash"}`,
			want: TaskProgressMessage{
				TaskID:       "t1",
				Description:  "d",
				Usage:        TaskUsage{TotalTokens: 10, ToolUses: 2, DurationMs: 3000},
				UUID:         "u1",
				SessionID:    "s1",
				ToolUseID:    "tu",
				LastToolName: "Bash",
			},
		},
		{
			name: "task_progress without usage decodes to zero value",
			line: `{"type":"system","subtype":"task_progress","task_id":"t1","description":"d","uuid":"u1","session_id":"s1"}`,
			want: TaskProgressMessage{
				TaskID:      "t1",
				Description: "d",
				UUID:        "u1",
				SessionID:   "s1",
			},
		},
		{
			name: "task_notification",
			line: `{"type":"system","subtype":"task_notification","task_id":"t1","status":"completed","output_file":"/tmp/o","summary":"done","uuid":"u1","session_id":"s1","tool_use_id":"tu","usage":{"total_tokens":5,"tool_uses":1,"duration_ms":100}}`,
			want: TaskNotificationMessage{
				TaskID:     "t1",
				Status:     "completed",
				OutputFile: "/tmp/o",
				Summary:    "done",
				UUID:       "u1",
				SessionID:  "s1",
				ToolUseID:  "tu",
				Usage:      TaskUsage{TotalTokens: 5, ToolUses: 1, DurationMs: 100},
			},
		},
		{
			name: "task_notification without optional fields",
			line: `{"type":"system","subtype":"task_notification","task_id":"t1","status":"failed","output_file":"","summary":"","uuid":"u1","session_id":"s1"}`,
			want: TaskNotificationMessage{
				TaskID:    "t1",
				Status:    "failed",
				UUID:      "u1",
				SessionID: "s1",
			},
		},
		{
			name: "task_updated with well-formed patch",
			line: `{"type":"system","subtype":"task_updated","task_id":"t1","patch":{"status":"running","progress":0.5},"session_id":"s1","uuid":"u1"}`,
			want: TaskUpdatedMessage{
				TaskID:    "t1",
				Patch:     map[string]any{"status": "running", "progress": float64(0.5)},
				Status:    "running",
				SessionID: "s1",
				UUID:      "u1",
			},
		},
		{
			name: "task_updated with absent patch",
			line: `{"type":"system","subtype":"task_updated","session_id":"s1","uuid":"u1"}`,
			want: TaskUpdatedMessage{
				Patch:     map[string]any{},
				SessionID: "s1",
				UUID:      "u1",
			},
		},
		{
			name: "task_updated with malformed patch",
			line: `{"type":"system","subtype":"task_updated","patch":"not-an-object","session_id":"s1","uuid":"u1"}`,
			want: TaskUpdatedMessage{
				Patch:     map[string]any{},
				SessionID: "s1",
				UUID:      "u1",
			},
		},
		{
			name: "task_updated with non-string patch status",
			line: `{"type":"system","subtype":"task_updated","task_id":"t1","patch":{"status":7},"session_id":"s1","uuid":"u1"}`,
			want: TaskUpdatedMessage{
				TaskID:    "t1",
				Patch:     map[string]any{"status": float64(7)},
				SessionID: "s1",
				UUID:      "u1",
			},
		},
		{
			name: "task_updated with absent task_id",
			line: `{"type":"system","subtype":"task_updated","patch":{"status":"completed"},"session_id":"s1","uuid":"u1"}`,
			want: TaskUpdatedMessage{
				Patch:     map[string]any{"status": "completed"},
				Status:    "completed",
				SessionID: "s1",
				UUID:      "u1",
			},
		},
		{
			name: "hook_started reads hook_event",
			line: `{"type":"system","subtype":"hook_started","hook_event":"PreToolUse","session_id":"s1","uuid":"u1"}`,
			want: HookEventMessage{
				Subtype:       "hook_started",
				HookEventName: "PreToolUse",
				SessionID:     "s1",
				UUID:          "u1",
			},
		},
		{
			name: "hook_response reads hook_name when hook_event absent",
			line: `{"type":"system","subtype":"hook_response","hook_name":"Stop","session_id":"s1","uuid":"u1"}`,
			want: HookEventMessage{
				Subtype:       "hook_response",
				HookEventName: "Stop",
				SessionID:     "s1",
				UUID:          "u1",
			},
		},
		{
			name: "hook_started reads hook_event_name when others absent",
			line: `{"type":"system","subtype":"hook_started","hook_event_name":"Notification","session_id":"s1","uuid":"u1"}`,
			want: HookEventMessage{
				Subtype:       "hook_started",
				HookEventName: "Notification",
				SessionID:     "s1",
				UUID:          "u1",
			},
		},
		{
			name: "hook_started prefers hook_event over hook_name",
			line: `{"type":"system","subtype":"hook_started","hook_event":"PreToolUse","hook_name":"PostToolUse","session_id":"s1","uuid":"u1"}`,
			want: HookEventMessage{
				Subtype:       "hook_started",
				HookEventName: "PreToolUse",
				SessionID:     "s1",
				UUID:          "u1",
			},
		},
		{
			name: "unmodeled subtype still decodes to generic SystemMessage",
			line: `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"s1"}`,
			want: SystemMessage{
				Subtype: "init",
				Raw:     json.RawMessage(`{"type":"system","subtype":"init","cwd":"/tmp","session_id":"s1"}`),
			},
		},
	})
}

func TestDecodeLine_ResultMessage(t *testing.T) {
	runDecodeLineTests(t, []decodeLineTestCase{
		{
			name: "modelUsage keyed by model string",
			line: `{"type":"result","subtype":"success","duration_ms":10,"duration_api_ms":7,"is_error":false,"num_turns":2,"session_id":"s1","result":"done","uuid":"u1","terminal_reason":"end_turn","api_error_status":0,"errors":null,"modelUsage":{"claude-sonnet-4":{"inputTokens":100,"outputTokens":50,"cacheReadInputTokens":25,"cacheCreationInputTokens":10,"webSearchRequests":2,"costUSD":0.05,"contextWindow":200000,"maxOutputTokens":8192,"canonicalModel":"claude-sonnet-4","provider":"firstParty"}},"deferred_tool_use":null}`,
			want: ResultMessage{
				DurationMs:     10,
				DurationAPIMs:  7,
				NumTurns:       2,
				SessionID:      "s1",
				Result:         "done",
				UUID:           "u1",
				TerminalReason: "end_turn",
				ModelUsage: map[string]ModelUsage{
					"claude-sonnet-4": {
						InputTokens:              100,
						OutputTokens:             50,
						CacheReadInputTokens:     25,
						CacheCreationInputTokens: 10,
						WebSearchRequests:        2,
						CostUSD:                  0.05,
						ContextWindow:            200000,
						MaxOutputTokens:          8192,
						CanonicalModel:           "claude-sonnet-4",
						Provider:                 "firstParty",
					},
				},
			},
		},
		{
			name: "deferred tool use, errors, api_error_status, structured_output",
			line: `{"type":"result","subtype":"error_max_turns","duration_ms":1,"is_error":true,"num_turns":3,"session_id":"s1","result":"","errors":["boom","again"],"api_error_status":529,"structured_output":{"answer":42},"deferred_tool_use":{"id":"d1","name":"puter","input":{"a":"b"}},"uuid":"u2"}`,
			want: ResultMessage{
				DurationMs:       1,
				IsError:          true,
				NumTurns:         3,
				SessionID:        "s1",
				Errors:           []string{"boom", "again"},
				APIErrorStatus:   529,
				StructuredOutput: json.RawMessage(`{"answer":42}`),
				DeferredToolUse: &DeferredToolUse{
					ID:    "d1",
					Name:  "puter",
					Input: map[string]any{"a": "b"},
				},
				UUID: "u2",
			},
		},
		{
			name: "legacy result without new fields decodes with zero values",
			line: `{"type":"result","subtype":"success","duration_ms":5,"is_error":false,"num_turns":1,"session_id":"s1","stop_reason":"end_turn","total_cost_usd":0.01,"result":"ok"}`,
			want: ResultMessage{
				DurationMs:   5,
				NumTurns:     1,
				SessionID:    "s1",
				StopReason:   "end_turn",
				TotalCostUSD: 0.01,
				Result:       "ok",
				UUID:         "",
			},
		},
	})
}

func TestDecodeLine_MalformedLinesStillError(t *testing.T) {
	for _, line := range []string{
		`not json`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":123}]}}`,
	} {
		if _, err := decodeLine([]byte(line)); err == nil {
			t.Errorf("decodeLine(%q) error = nil, want non-nil", line)
		}
	}

	if got, err := decodeLine([]byte("   ")); got != nil || err != nil {
		t.Errorf("decodeLine(blank) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestDecodeContentBlocks_Direct(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"server_tool_use","id":"a","name":"n","input":{"k":"v"}}`),
		json.RawMessage(`{"type":"advisor_tool_result","tool_use_id":"a","content":null}`),
		json.RawMessage(`{"type":"unmodeled"}`),
	}

	got, err := decodeContentBlocks(raw)
	if err != nil {
		t.Fatalf("decodeContentBlocks() error = %v", err)
	}

	want := []ContentBlock{
		ServerToolUseBlock{ID: "a", Name: "n", Input: map[string]any{"k": "v"}},
		ServerToolResultBlock{ToolUseID: "a", Content: json.RawMessage(`null`)},
		RawBlock{Type: "unmodeled", Raw: json.RawMessage(`{"type":"unmodeled"}`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeContentBlocks() = %#v, want %#v", got, want)
	}
}
