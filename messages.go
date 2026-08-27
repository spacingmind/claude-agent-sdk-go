package claudecode

import (
	"bytes"
	"encoding/json"
)

// Message is implemented by the message types the CLI can stream onto
// Client.Prompt's updates channel: SystemMessage (and its typed task/hook
// variants), AssistantMessage, UserMessage, ResultMessage, StreamEvent,
// RateLimitEvent, and ConversationResetMessage.
type Message interface {
	isMessage()
}

// SystemMessage is a lifecycle/init event from the CLI. Subtype is not
// modeled exhaustively -- Raw carries the full line for subtypes callers
// need to inspect themselves. Task lifecycle subtypes (task_started,
// task_progress, task_notification, task_updated) and hook events
// (hook_started, hook_response) decode to their own concrete types; this
// catch-all covers everything else.
type SystemMessage struct {
	Subtype string
	Raw     json.RawMessage
}

func (SystemMessage) isMessage() {}

// AssistantMessage carries one assistant turn's content blocks as they
// stream in.
type AssistantMessage struct {
	Content    []ContentBlock
	Model      string
	SessionID  string
	StopReason string
}

func (AssistantMessage) isMessage() {}

// UserMessage echoes a user-role turn back from the CLI, including tool
// results fed back to the model during multi-turn tool use.
type UserMessage struct {
	Content   []ContentBlock
	SessionID string
}

func (UserMessage) isMessage() {}

// ResultMessage is the terminal message for a prompt turn.
type ResultMessage struct {
	DurationMs        int64
	IsError           bool
	NumTurns          int
	SessionID         string
	StopReason        string
	TotalCostUSD      float64
	Result            string
	PermissionDenials []any
	DurationAPIMs     int64
	ModelUsage        map[string]ModelUsage
	DeferredToolUse   *DeferredToolUse
	Errors            []string
	APIErrorStatus    int
	TerminalReason    string
	StructuredOutput  json.RawMessage
	UUID              string
}

func (ResultMessage) isMessage() {}

// ModelUsage is the per-model token/cost breakdown the CLI reports in a
// ResultMessage's modelUsage field. Wire keys are camelCase (matching the
// TypeScript SDK's shape), unlike the snake_case keys used elsewhere in the
// result envelope.
type ModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	WebSearchRequests        int     `json:"webSearchRequests"`
	CostUSD                  float64 `json:"costUSD"`
	ContextWindow            int     `json:"contextWindow"`
	MaxOutputTokens          int     `json:"maxOutputTokens"`
	CanonicalModel           string  `json:"canonicalModel"`
	Provider                 string  `json:"provider"`
}

// DeferredToolUse is the tool use the CLI deferred past the end of the turn
// (json-schema deferred tool calling), reported on the ResultMessage.
type DeferredToolUse struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// StreamEvent is a raw Anthropic API stream event, passed through
// undecomposed. Only emitted when the partial-messages option is enabled.
type StreamEvent struct {
	UUID            string
	SessionID       string
	Event           json.RawMessage
	ParentToolUseID string
}

func (StreamEvent) isMessage() {}

// RateLimitEvent reports the CLI's rate-limit state.
type RateLimitEvent struct {
	RateLimitInfo RateLimitInfo
	UUID          string
	SessionID     string
}

func (RateLimitEvent) isMessage() {}

// RateLimitInfo carries the optional fields of a rate_limit_event. All wire
// fields are optional; an absent field decodes to its zero value.
type RateLimitInfo struct {
	Status                string  `json:"status"`
	ResetsAt              int64   `json:"resetsAt"`
	RateLimitType         string  `json:"rateLimitType"`
	Utilization           float64 `json:"utilization"`
	OverageStatus         string  `json:"overageStatus"`
	OverageResetsAt       int64   `json:"overageResetsAt"`
	OverageDisabledReason string  `json:"overageDisabledReason"`
}

// ConversationResetMessage reports that the CLI reset the conversation.
type ConversationResetMessage struct {
	NewConversationID string
	UUID              string
	SessionID         string
}

func (ConversationResetMessage) isMessage() {}

// TaskUsage is the usage block reported on task_progress and
// task_notification messages.
type TaskUsage struct {
	TotalTokens int   `json:"total_tokens"`
	ToolUses    int   `json:"tool_uses"`
	DurationMs  int64 `json:"duration_ms"`
}

// TaskStartedMessage is a system message with subtype "task_started".
// ToolUseID and TaskType are optional and default to their zero values.
type TaskStartedMessage struct {
	TaskID      string
	Description string
	UUID        string
	SessionID   string
	ToolUseID   string
	TaskType    string
}

func (TaskStartedMessage) isMessage() {}

// TaskProgressMessage is a system message with subtype "task_progress".
// ToolUseID and LastToolName are optional and default to their zero values.
type TaskProgressMessage struct {
	TaskID       string
	Description  string
	Usage        TaskUsage
	UUID         string
	SessionID    string
	ToolUseID    string
	LastToolName string
}

func (TaskProgressMessage) isMessage() {}

// TaskNotificationMessage is a system message with subtype
// "task_notification". ToolUseID and Usage are optional and default to
// their zero values.
type TaskNotificationMessage struct {
	TaskID     string
	Status     string
	OutputFile string
	Summary    string
	UUID       string
	SessionID  string
	ToolUseID  string
	Usage      TaskUsage
}

func (TaskNotificationMessage) isMessage() {}

// TaskUpdatedMessage is a system message with subtype "task_updated",
// parsed defensively: TaskID defaults to "", Patch to an empty map, and
// Status is derived from patch["status"] when present.
type TaskUpdatedMessage struct {
	TaskID    string
	Patch     map[string]any
	Status    string
	SessionID string
	UUID      string
}

func (TaskUpdatedMessage) isMessage() {}

// HookEventMessage is a system message with subtype "hook_started" or
// "hook_response", emitted when the include-hook-events option is enabled.
// HookEventName is read from hook_event, hook_name, or hook_event_name,
// whichever is present first.
type HookEventMessage struct {
	Subtype       string
	HookEventName string
	SessionID     string
	UUID          string
}

func (HookEventMessage) isMessage() {}

// ContentBlock is implemented by TextBlock, ThinkingBlock, ToolUseBlock,
// ToolResultBlock, ServerToolUseBlock, ServerToolResultBlock, and RawBlock
// (the catch-all for block types this package doesn't model).
type ContentBlock interface {
	isContentBlock()
}

// TextBlock is a plain-text content block.
type TextBlock struct {
	Text string
}

func (TextBlock) isContentBlock() {}

// ThinkingBlock is an extended-thinking block.
type ThinkingBlock struct {
	Thinking  string
	Signature string
}

func (ThinkingBlock) isContentBlock() {}

// ToolUseBlock is a tool invocation the assistant requested.
type ToolUseBlock struct {
	ID    string
	Name  string
	Input map[string]any
}

func (ToolUseBlock) isContentBlock() {}

// ToolResultBlock is a tool result. It appears inside both assistant and
// user message content. Content is a raw passthrough because it can be
// either a plain string or an array of blocks.
type ToolResultBlock struct {
	ToolUseID string
	Content   json.RawMessage
	IsError   bool
}

func (ToolResultBlock) isContentBlock() {}

// ServerToolUseBlock is a tool invocation the CLI executed server-side.
type ServerToolUseBlock struct {
	ID    string
	Name  string
	Input map[string]any
}

func (ServerToolUseBlock) isContentBlock() {}

// ServerToolResultBlock is the result of a ServerToolUseBlock. The wire
// discriminator is "advisor_tool_result", not "server_tool_result" -- the
// name deliberately differs from the type, matching the upstream SDKs.
type ServerToolResultBlock struct {
	ToolUseID string
	Content   json.RawMessage
}

func (ServerToolResultBlock) isContentBlock() {}

// RawBlock passes through a content block of a type this package doesn't
// model, so unrecognized block types don't break parsing of the rest of the
// message.
type RawBlock struct {
	Type string
	Raw  json.RawMessage
}

func (RawBlock) isContentBlock() {}

// controlRequest is the CLI's request for a permission (or other control)
// decision. It is protocol-internal: Client.Prompt handles it via the
// configured PermissionPolicy and never surfaces it on the updates channel.
type controlRequest struct {
	RequestID             string
	Subtype               string
	ToolName              string
	Input                 map[string]any
	ToolUseID             string
	PermissionSuggestions []any
}

// decodeLine parses one line of the CLI's NDJSON stdout. It returns
// (nil, nil) for blank lines and message types this package doesn't act on
// (forward-compatible skip), and (nil, err) for a line that looks like JSON
// but fails to parse or is missing fields this package requires -- callers
// should skip such a line rather than fail the whole turn over it, since a
// single malformed line from the CLI shouldn't abort an otherwise-healthy
// run.
func decodeLine(raw []byte) (any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}

	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}

	switch env.Type {
	case "system":
		return decodeSystemMessage(raw)
	case "assistant":
		return decodeAssistantMessage(raw)
	case "user":
		return decodeUserMessage(raw)
	case "result":
		return decodeResultMessage(raw)
	case "stream_event":
		return decodeStreamEvent(raw)
	case "rate_limit_event":
		return decodeRateLimitEvent(raw)
	case "conversation_reset":
		return decodeConversationReset(raw)
	case "control_request":
		return decodeControlRequest(raw)
	default:
		return nil, nil
	}
}

func decodeSystemMessage(raw []byte) (any, error) {
	var w struct {
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	switch w.Subtype {
	case "task_started":
		return decodeTaskStarted(raw)
	case "task_progress":
		return decodeTaskProgress(raw)
	case "task_notification":
		return decodeTaskNotification(raw)
	case "task_updated":
		return decodeTaskUpdated(raw)
	case "hook_started", "hook_response":
		return decodeHookEvent(raw, w.Subtype)
	default:
		return SystemMessage{Subtype: w.Subtype, Raw: json.RawMessage(raw)}, nil
	}
}

func decodeTaskStarted(raw []byte) (any, error) {
	var w struct {
		TaskID      string `json:"task_id"`
		Description string `json:"description"`
		UUID        string `json:"uuid"`
		SessionID   string `json:"session_id"`
		ToolUseID   string `json:"tool_use_id"`
		TaskType    string `json:"task_type"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return TaskStartedMessage{
		TaskID:      w.TaskID,
		Description: w.Description,
		UUID:        w.UUID,
		SessionID:   w.SessionID,
		ToolUseID:   w.ToolUseID,
		TaskType:    w.TaskType,
	}, nil
}

func decodeTaskProgress(raw []byte) (any, error) {
	var w struct {
		TaskID       string    `json:"task_id"`
		Description  string    `json:"description"`
		Usage        TaskUsage `json:"usage"`
		UUID         string    `json:"uuid"`
		SessionID    string    `json:"session_id"`
		ToolUseID    string    `json:"tool_use_id"`
		LastToolName string    `json:"last_tool_name"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return TaskProgressMessage{
		TaskID:       w.TaskID,
		Description:  w.Description,
		Usage:        w.Usage,
		UUID:         w.UUID,
		SessionID:    w.SessionID,
		ToolUseID:    w.ToolUseID,
		LastToolName: w.LastToolName,
	}, nil
}

func decodeTaskNotification(raw []byte) (any, error) {
	var w struct {
		TaskID     string    `json:"task_id"`
		Status     string    `json:"status"`
		OutputFile string    `json:"output_file"`
		Summary    string    `json:"summary"`
		UUID       string    `json:"uuid"`
		SessionID  string    `json:"session_id"`
		ToolUseID  string    `json:"tool_use_id"`
		Usage      TaskUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return TaskNotificationMessage{
		TaskID:     w.TaskID,
		Status:     w.Status,
		OutputFile: w.OutputFile,
		Summary:    w.Summary,
		UUID:       w.UUID,
		SessionID:  w.SessionID,
		ToolUseID:  w.ToolUseID,
		Usage:      w.Usage,
	}, nil
}

// decodeTaskUpdated never fails on a malformed or absent patch: TaskID
// defaults to "", Patch to an empty map, and Status is read from
// patch["status"] only when it is a string.
func decodeTaskUpdated(raw []byte) (any, error) {
	var w struct {
		TaskID    string          `json:"task_id"`
		Patch     json.RawMessage `json:"patch"`
		SessionID string          `json:"session_id"`
		UUID      string          `json:"uuid"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	patch := map[string]any{}
	if len(w.Patch) > 0 {
		var p map[string]any
		if err := json.Unmarshal(w.Patch, &p); err == nil && p != nil {
			patch = p
		}
	}
	status := ""
	if s, ok := patch["status"].(string); ok {
		status = s
	}
	return TaskUpdatedMessage{
		TaskID:    w.TaskID,
		Patch:     patch,
		Status:    status,
		SessionID: w.SessionID,
		UUID:      w.UUID,
	}, nil
}

func decodeHookEvent(raw []byte, subtype string) (any, error) {
	var w struct {
		HookEvent     string `json:"hook_event"`
		HookName      string `json:"hook_name"`
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
		UUID          string `json:"uuid"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	name := w.HookEvent
	if name == "" {
		name = w.HookName
	}
	if name == "" {
		name = w.HookEventName
	}
	return HookEventMessage{
		Subtype:       subtype,
		HookEventName: name,
		SessionID:     w.SessionID,
		UUID:          w.UUID,
	}, nil
}

func decodeAssistantMessage(raw []byte) (any, error) {
	var w struct {
		SessionID string `json:"session_id"`
		Message   struct {
			Model      string            `json:"model"`
			StopReason string            `json:"stop_reason"`
			Content    []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	blocks, err := decodeContentBlocks(w.Message.Content)
	if err != nil {
		return nil, err
	}
	return AssistantMessage{
		Content:    blocks,
		Model:      w.Message.Model,
		SessionID:  w.SessionID,
		StopReason: w.Message.StopReason,
	}, nil
}

func decodeUserMessage(raw []byte) (any, error) {
	var w struct {
		SessionID string `json:"session_id"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}

	var asArray []json.RawMessage
	var blocks []ContentBlock
	if err := json.Unmarshal(w.Message.Content, &asArray); err == nil {
		blocks, err = decodeContentBlocks(asArray)
		if err != nil {
			return nil, err
		}
	} else {
		var asString string
		if err := json.Unmarshal(w.Message.Content, &asString); err != nil {
			return nil, err
		}
		blocks = []ContentBlock{TextBlock{Text: asString}}
	}

	return UserMessage{Content: blocks, SessionID: w.SessionID}, nil
}

func decodeStreamEvent(raw []byte) (any, error) {
	var w struct {
		UUID            string          `json:"uuid"`
		SessionID       string          `json:"session_id"`
		Event           json.RawMessage `json:"event"`
		ParentToolUseID string          `json:"parent_tool_use_id"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return StreamEvent{
		UUID:            w.UUID,
		SessionID:       w.SessionID,
		Event:           w.Event,
		ParentToolUseID: w.ParentToolUseID,
	}, nil
}

func decodeRateLimitEvent(raw []byte) (any, error) {
	var w struct {
		RateLimitInfo RateLimitInfo `json:"rate_limit_info"`
		UUID          string        `json:"uuid"`
		SessionID     string        `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return RateLimitEvent{
		RateLimitInfo: w.RateLimitInfo,
		UUID:          w.UUID,
		SessionID:     w.SessionID,
	}, nil
}

func decodeConversationReset(raw []byte) (any, error) {
	var w struct {
		NewConversationID string `json:"new_conversation_id"`
		UUID              string `json:"uuid"`
		SessionID         string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return ConversationResetMessage{
		NewConversationID: w.NewConversationID,
		UUID:              w.UUID,
		SessionID:         w.SessionID,
	}, nil
}

func decodeContentBlocks(raw []json.RawMessage) ([]ContentBlock, error) {
	blocks := make([]ContentBlock, 0, len(raw))
	for _, r := range raw {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(r, &head); err != nil {
			return nil, err
		}
		switch head.Type {
		case "text":
			var b struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(r, &b); err != nil {
				return nil, err
			}
			blocks = append(blocks, TextBlock{Text: b.Text})
		case "thinking":
			var b struct {
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
			}
			if err := json.Unmarshal(r, &b); err != nil {
				return nil, err
			}
			blocks = append(blocks, ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature})
		case "tool_use":
			var b struct {
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			}
			if err := json.Unmarshal(r, &b); err != nil {
				return nil, err
			}
			blocks = append(blocks, ToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input})
		case "tool_result":
			var b struct {
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			}
			if err := json.Unmarshal(r, &b); err != nil {
				return nil, err
			}
			blocks = append(blocks, ToolResultBlock{ToolUseID: b.ToolUseID, Content: b.Content, IsError: b.IsError})
		case "server_tool_use":
			var b struct {
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			}
			if err := json.Unmarshal(r, &b); err != nil {
				return nil, err
			}
			blocks = append(blocks, ServerToolUseBlock{ID: b.ID, Name: b.Name, Input: b.Input})
		case "advisor_tool_result":
			var b struct {
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(r, &b); err != nil {
				return nil, err
			}
			blocks = append(blocks, ServerToolResultBlock{ToolUseID: b.ToolUseID, Content: b.Content})
		default:
			blocks = append(blocks, RawBlock{Type: head.Type, Raw: json.RawMessage(r)})
		}
	}
	return blocks, nil
}

func decodeResultMessage(raw []byte) (any, error) {
	var w struct {
		DurationMs        int64                 `json:"duration_ms"`
		IsError           bool                  `json:"is_error"`
		NumTurns          int                   `json:"num_turns"`
		SessionID         string                `json:"session_id"`
		StopReason        string                `json:"stop_reason"`
		TotalCostUSD      float64               `json:"total_cost_usd"`
		Result            string                `json:"result"`
		PermissionDenials []any                 `json:"permission_denials"`
		DurationAPIMs     int64                 `json:"duration_api_ms"`
		ModelUsage        map[string]ModelUsage `json:"modelUsage"`
		DeferredToolUse   *DeferredToolUse      `json:"deferred_tool_use"`
		Errors            []string              `json:"errors"`
		APIErrorStatus    int                   `json:"api_error_status"`
		TerminalReason    string                `json:"terminal_reason"`
		StructuredOutput  json.RawMessage       `json:"structured_output"`
		UUID              string                `json:"uuid"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return ResultMessage{
		DurationMs:        w.DurationMs,
		IsError:           w.IsError,
		NumTurns:          w.NumTurns,
		SessionID:         w.SessionID,
		StopReason:        w.StopReason,
		TotalCostUSD:      w.TotalCostUSD,
		Result:            w.Result,
		PermissionDenials: w.PermissionDenials,
		DurationAPIMs:     w.DurationAPIMs,
		ModelUsage:        w.ModelUsage,
		DeferredToolUse:   w.DeferredToolUse,
		Errors:            w.Errors,
		APIErrorStatus:    w.APIErrorStatus,
		TerminalReason:    w.TerminalReason,
		StructuredOutput:  w.StructuredOutput,
		UUID:              w.UUID,
	}, nil
}

func decodeControlRequest(raw []byte) (any, error) {
	var w struct {
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	var inner struct {
		Subtype               string         `json:"subtype"`
		ToolName              string         `json:"tool_name"`
		Input                 map[string]any `json:"input"`
		ToolUseID             string         `json:"tool_use_id"`
		PermissionSuggestions []any          `json:"permission_suggestions"`
	}
	if err := json.Unmarshal(w.Request, &inner); err != nil {
		return nil, err
	}
	return &controlRequest{
		RequestID:             w.RequestID,
		Subtype:               inner.Subtype,
		ToolName:              inner.ToolName,
		Input:                 inner.Input,
		ToolUseID:             inner.ToolUseID,
		PermissionSuggestions: inner.PermissionSuggestions,
	}, nil
}
