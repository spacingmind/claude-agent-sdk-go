package claudecode

import "context"

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

// PermissionPolicy decides can_use_tool control requests the CLI sends
// mid-turn. updatedInput, when non-nil on an allow decision, replaces the
// tool's input before it runs; denyMessage is surfaced to the model as the
// reason on a deny decision; updatedPermissions, when non-nil on an allow
// decision, is passed back to the CLI as session permission updates;
// interrupt, when true on a deny decision, asks the CLI to abort the turn.
// No UI exists yet to drive this decision interactively -- this interface
// is the seam a future UI-backed implementation plugs into.
type PermissionPolicy interface {
	Decide(ctx context.Context, req CanUseToolRequest) (allow bool, updatedInput map[string]any, denyMessage string, updatedPermissions []map[string]any, interrupt bool, err error)
}

// AutoApprovePolicy allows every tool use unchanged. Useful for exercising
// the pipeline end to end without a human in the loop.
type AutoApprovePolicy struct{}

func (AutoApprovePolicy) Decide(_ context.Context, req CanUseToolRequest) (bool, map[string]any, string, []map[string]any, bool, error) {
	return true, req.Input, "", nil, false, nil
}

// AutoDenyPolicy denies every tool use. The safe default in the absence of
// a UI: see New's doc comment for why this, not AutoApprovePolicy, is what
// New uses when the caller doesn't supply a policy.
type AutoDenyPolicy struct{}

func (AutoDenyPolicy) Decide(_ context.Context, _ CanUseToolRequest) (bool, map[string]any, string, []map[string]any, bool, error) {
	return false, nil, "denied: no permission UI is wired up yet", nil, false, nil
}

// HookCallback is the dispatch seam for hook_callback control requests:
// looked up by the callback ID the CLI sends, invoked with the request's
// input, and its returned map is written back verbatim as response_data.
// Deliberately loosely typed -- a future hooks phase adds the typed
// hook-input variants and the public registration API.
type HookCallback func(ctx context.Context, input map[string]any, toolUseID string) (map[string]any, error)
