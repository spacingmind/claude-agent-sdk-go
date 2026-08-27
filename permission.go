package claudecode

import "context"

// CanUseToolRequest is the CLI's can_use_tool control request, asking
// whether a specific tool invocation should proceed.
type CanUseToolRequest struct {
	ToolName              string
	Input                 map[string]any
	ToolUseID             string
	PermissionSuggestions []any
}

// PermissionPolicy decides can_use_tool control requests the CLI sends
// mid-turn. updatedInput, when non-nil on an allow decision, replaces the
// tool's input before it runs; denyMessage is surfaced to the model as the
// reason on a deny decision. No UI exists yet to drive this decision
// interactively -- this interface is the seam a future UI-backed
// implementation plugs into.
type PermissionPolicy interface {
	Decide(ctx context.Context, req CanUseToolRequest) (allow bool, updatedInput map[string]any, denyMessage string, err error)
}

// AutoApprovePolicy allows every tool use unchanged. Useful for exercising
// the pipeline end to end without a human in the loop.
type AutoApprovePolicy struct{}

func (AutoApprovePolicy) Decide(_ context.Context, req CanUseToolRequest) (bool, map[string]any, string, error) {
	return true, req.Input, "", nil
}

// AutoDenyPolicy denies every tool use. The safe default in the absence of
// a UI: see New's doc comment for why this, not AutoApprovePolicy, is what
// New uses when the caller doesn't supply a policy.
type AutoDenyPolicy struct{}

func (AutoDenyPolicy) Decide(_ context.Context, _ CanUseToolRequest) (bool, map[string]any, string, error) {
	return false, nil, "denied: no permission UI is wired up yet", nil
}
