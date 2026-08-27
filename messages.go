package claudecode

import (
	"bytes"
	"encoding/json"
)

// Message is implemented by the message types the CLI can stream onto
// Client.Prompt's updates channel: SystemMessage, AssistantMessage,
// UserMessage, and ResultMessage.
type Message interface {
	isMessage()
}

// SystemMessage is a lifecycle/init event from the CLI. Subtype is not
// modeled exhaustively -- Raw carries the full line for subtypes callers
// need to inspect themselves.
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
}

func (ResultMessage) isMessage() {}

// ContentBlock is implemented by TextBlock, ToolUseBlock, and RawBlock (the
// catch-all for block types this package doesn't model, e.g. "thinking" or
// "tool_result").
type ContentBlock interface {
	isContentBlock()
}

// TextBlock is a plain-text content block.
type TextBlock struct {
	Text string
}

func (TextBlock) isContentBlock() {}

// ToolUseBlock is a tool invocation the assistant requested.
type ToolUseBlock struct {
	ID    string
	Name  string
	Input map[string]any
}

func (ToolUseBlock) isContentBlock() {}

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
	return SystemMessage{Subtype: w.Subtype, Raw: json.RawMessage(raw)}, nil
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
		default:
			blocks = append(blocks, RawBlock{Type: head.Type, Raw: json.RawMessage(r)})
		}
	}
	return blocks, nil
}

func decodeResultMessage(raw []byte) (any, error) {
	var w struct {
		DurationMs        int64   `json:"duration_ms"`
		IsError           bool    `json:"is_error"`
		NumTurns          int     `json:"num_turns"`
		SessionID         string  `json:"session_id"`
		StopReason        string  `json:"stop_reason"`
		TotalCostUSD      float64 `json:"total_cost_usd"`
		Result            string  `json:"result"`
		PermissionDenials []any   `json:"permission_denials"`
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
