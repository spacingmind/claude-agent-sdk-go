package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CanUseToolRequest is the CLI's can_use_tool control request, asking
// whether a specific tool invocation should proceed.
type CanUseToolRequest struct {
	ToolName              string
	Input                 map[string]any
	ToolUseID             string
	PermissionSuggestions []any
	Title                 string
	DisplayName           string
	Description           string
	DecisionReason        string
	BlockedPath           string
	AgentID               string
}

// PermissionRuleValue is one rule entry inside a rules-variant
// PermissionUpdate. RuleContent is omitted from the wire when empty.
type PermissionRuleValue struct {
	ToolName    string
	RuleContent string // empty = omitted on the wire
}

// PermissionUpdate is a typed session permission update, matching the
// Python reference SDK's PermissionUpdate. Only the fields relevant to
// Type are sent on the wire (see MarshalJSON); irrelevant fields set on
// the Go struct are silently dropped, not validated.
type PermissionUpdate struct {
	Type        string                // "addRules" | "replaceRules" | "removeRules" | "setMode" | "addDirectories" | "removeDirectories"
	Rules       []PermissionRuleValue // addRules/replaceRules/removeRules only
	Behavior    string                // "allow" | "deny" | "ask"; rules-variants only
	Mode        string                // setMode only
	Directories []string              // addDirectories/removeDirectories only
	Destination string                // optional on every variant
}

// MarshalJSON emits only the fields relevant to Type, mirroring the
// Python reference's to_dict().
func (u PermissionUpdate) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": u.Type}

	switch u.Type {
	case "addRules", "replaceRules", "removeRules":
		rules := make([]map[string]any, 0, len(u.Rules))
		for _, r := range u.Rules {
			rm := map[string]any{"toolName": r.ToolName}
			if r.RuleContent != "" {
				rm["ruleContent"] = r.RuleContent
			}

			rules = append(rules, rm)
		}

		m["rules"] = rules
		m["behavior"] = u.Behavior
	case "setMode":
		m["mode"] = u.Mode
	case "addDirectories", "removeDirectories":
		m["directories"] = u.Directories
	}

	if u.Destination != "" {
		m["destination"] = u.Destination
	}

	return json.Marshal(m)
}

// PermissionPolicy decides can_use_tool control requests the CLI sends
// mid-turn. updatedInput, when non-nil on an allow decision, replaces the
// tool's input before it runs; denyMessage is surfaced to the model as the
// reason on a deny decision; updatedPermissions, when non-nil on an allow
// decision, is passed back to the CLI as session permission updates;
// interrupt, when true on a deny decision, asks the CLI to abort the turn.
// No UI exists yet to drive this decision interactively -- this interface
// is the seam a future UI-backed implementation plugs into.
type PermissionPolicy interface {
	Decide(ctx context.Context, req CanUseToolRequest) (allow bool, updatedInput map[string]any, denyMessage string, updatedPermissions []PermissionUpdate, interrupt bool, err error)
}

// AutoApprovePolicy allows every tool use unchanged. Useful for exercising
// the pipeline end to end without a human in the loop.
type AutoApprovePolicy struct{}

// Decide implements PermissionPolicy: always allow, passing the request input
// through unchanged.
func (AutoApprovePolicy) Decide(_ context.Context, req CanUseToolRequest) (bool, map[string]any, string, []PermissionUpdate, bool, error) {
	return true, req.Input, "", nil, false, nil
}

// AutoDenyPolicy denies every tool use. The safe default in the absence of
// a UI: see New's doc comment for why this, not AutoApprovePolicy, is what
// New uses when the caller doesn't supply a policy.
type AutoDenyPolicy struct{}

// Decide implements PermissionPolicy: always deny with a fixed message.
func (AutoDenyPolicy) Decide(_ context.Context, _ CanUseToolRequest) (bool, map[string]any, string, []PermissionUpdate, bool, error) {
	return false, nil, "denied: no permission UI is wired up yet", nil, false, nil
}

// HookEvent names a point in the CLI's lifecycle where hook callbacks fire.
type HookEvent string

// Hook events, matching the CLI's hook event names.
const (
	HookEventPreToolUse         HookEvent = "PreToolUse"
	HookEventPostToolUse        HookEvent = "PostToolUse"
	HookEventPostToolUseFailure HookEvent = "PostToolUseFailure"
	HookEventUserPromptSubmit   HookEvent = "UserPromptSubmit"
	HookEventStop               HookEvent = "Stop"
	HookEventSubagentStop       HookEvent = "SubagentStop"
	HookEventPreCompact         HookEvent = "PreCompact"
	HookEventNotification       HookEvent = "Notification"
	HookEventSubagentStart      HookEvent = "SubagentStart"
	HookEventPermissionRequest  HookEvent = "PermissionRequest"
)

// HookMatcher registers one or more callbacks for a HookEvent, optionally
// restricted to tool names matching Matcher and bounded by Timeout.
type HookMatcher struct {
	Matcher string // tool-name pattern, e.g. "Bash" or "Write|Edit"; empty matches all
	Hooks   []HookCallback
	Timeout time.Duration // 0 = no explicit timeout sent (CLI default applies)
}

// HookCallback is invoked when the CLI sends a hook_callback control
// request for an ID minted at initialize time. toolUseID is the request's
// tool_use_id (empty when the event carries none). A nil *HookJSONOutput
// with a nil error means "no output".
type HookCallback func(ctx context.Context, input map[string]any, toolUseID string) (*HookJSONOutput, error)

// HookJSONOutput is a hook callback's return value, matching the CLI's
// hook-output wire shape.
type HookJSONOutput struct {
	Continue           *bool          `json:"continue,omitempty"`
	SuppressOutput     bool           `json:"suppressOutput,omitempty"`
	StopReason         string         `json:"stopReason,omitempty"`
	Decision           string         `json:"decision,omitempty"`
	SystemMessage      string         `json:"systemMessage,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	HookSpecificOutput map[string]any `json:"hookSpecificOutput,omitempty"`
	Async              bool           `json:"async,omitempty"`
	AsyncTimeout       int            `json:"asyncTimeout,omitempty"`
}

// Common fields shared by every hook-input variant (see the CLI's hook
// input schema). The dispatch mechanism passes the raw map to callbacks;
// these typed views are opt-in via DecodeHookInput.
type hookInputBase struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permission_mode,omitempty"`
	HookEventName  string `json:"hook_event_name"`
}

type hookInputAgent struct {
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
}

// PreToolUseHookInput is the input for PreToolUse hook callbacks.
type PreToolUseHookInput struct {
	hookInputBase
	hookInputAgent
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	ToolUseID string         `json:"tool_use_id"`
}

// PostToolUseHookInput is the input for PostToolUse hook callbacks.
type PostToolUseHookInput struct {
	hookInputBase
	hookInputAgent
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse any            `json:"tool_response"`
	ToolUseID    string         `json:"tool_use_id"`
}

// PostToolUseFailureHookInput is the input for PostToolUseFailure hook
// callbacks.
type PostToolUseFailureHookInput struct {
	hookInputBase
	hookInputAgent
	ToolName    string         `json:"tool_name"`
	ToolInput   map[string]any `json:"tool_input"`
	ToolUseID   string         `json:"tool_use_id"`
	Error       string         `json:"error"`
	IsInterrupt bool           `json:"is_interrupt,omitempty"`
}

// UserPromptSubmitHookInput is the input for UserPromptSubmit hook
// callbacks.
type UserPromptSubmitHookInput struct {
	hookInputBase
	Prompt string `json:"prompt"`
}

// StopHookInput is the input for Stop hook callbacks.
type StopHookInput struct {
	hookInputBase
	StopHookActive bool `json:"stop_hook_active"`
}

// SubagentStopHookInput is the input for SubagentStop hook callbacks.
type SubagentStopHookInput struct {
	hookInputBase
	StopHookActive      bool   `json:"stop_hook_active"`
	AgentID             string `json:"agent_id"`
	AgentTranscriptPath string `json:"agent_transcript_path"`
	AgentType           string `json:"agent_type"`
}

// PreCompactHookInput is the input for PreCompact hook callbacks. Trigger
// is "manual" or "auto".
type PreCompactHookInput struct {
	hookInputBase
	Trigger            string `json:"trigger"`
	CustomInstructions string `json:"custom_instructions,omitempty"`
}

// NotificationHookInput is the input for Notification hook callbacks.
type NotificationHookInput struct {
	hookInputBase
	Message          string `json:"message"`
	Title            string `json:"title,omitempty"`
	NotificationType string `json:"notification_type"`
}

// SubagentStartHookInput is the input for SubagentStart hook callbacks.
type SubagentStartHookInput struct {
	hookInputBase
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// PermissionRequestHookInput is the input for PermissionRequest hook
// callbacks.
type PermissionRequestHookInput struct {
	hookInputBase
	hookInputAgent
	ToolName              string         `json:"tool_name"`
	ToolInput             map[string]any `json:"tool_input"`
	PermissionSuggestions []any          `json:"permission_suggestions,omitempty"`
}

// DecodeHookInput unmarshals a hook callback's raw input map into a typed
// hook-input struct (e.g. PreToolUseHookInput), for callbacks that want a
// typed view instead of working with the raw map directly.
func DecodeHookInput[T any](input map[string]any) (T, error) {
	var out T

	b, err := json.Marshal(input)
	if err != nil {
		return out, fmt.Errorf("claudecode: encode hook input: %w", err)
	}

	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("claudecode: decode hook input: %w", err)
	}

	return out, nil
}
