// Package claudecode is a native Go client for Claude Code's headless
// programmatic mode: it spawns the `claude` CLI with
// --output-format/--input-format stream-json and speaks its newline-
// delimited JSON wire protocol directly, including the control-request
// permission handshake. It is not the Agent Client Protocol -- Claude Code
// has its own wire format, distinct from ACP's JSON-RPC envelope.
package claudecode

import (
	"context"
	"fmt"
	"io"
	"time"
)

const defaultCloseGracePeriod = 5 * time.Second

// Option configures a Client constructed by New.
type Option func(*options)

type options struct {
	permissionMode   string
	permissionPolicy PermissionPolicy
	logWriter        io.Writer
	cliPath          string
	extraEnv         []string
	closeGracePeriod time.Duration
}

// WithPermissionMode sets the CLI's --permission-mode flag (e.g. "default",
// "acceptEdits", "bypassPermissions", "plan"). Even with a mode set, the CLI
// can still send can_use_tool control requests for decisions the mode
// doesn't cover -- see WithPermissionPolicy.
func WithPermissionMode(mode string) Option {
	return func(o *options) { o.permissionMode = mode }
}

// WithPermissionPolicy sets the policy that decides can_use_tool control
// requests the CLI sends mid-turn.
func WithPermissionPolicy(p PermissionPolicy) Option {
	return func(o *options) { o.permissionPolicy = p }
}

// WithLogWriter directs the CLI subprocess's stderr to w.
func WithLogWriter(w io.Writer) Option {
	return func(o *options) { o.logWriter = w }
}

// WithCLIPath overrides the "claude" binary looked up on PATH, e.g. to
// point at a specific install.
func WithCLIPath(path string) Option {
	return func(o *options) { o.cliPath = path }
}

// withExtraEnv and withCloseGracePeriod are test-only knobs (unexported: no
// production caller needs to override the subprocess environment or the
// close grace period, but the fake-CLI test harness needs both).
func withExtraEnv(env []string) Option {
	return func(o *options) { o.extraEnv = env }
}

func withCloseGracePeriod(d time.Duration) Option {
	return func(o *options) { o.closeGracePeriod = d }
}

// Client drives one claude CLI subprocess rooted at a task's git worktree.
type Client struct {
	tr               *transport
	permissionPolicy PermissionPolicy
	closeGracePeriod time.Duration
}

// New spawns `claude` with its working directory set to worktreePath, ready
// to receive prompts via Prompt.
//
// Defaults: permission mode "default" (the CLI's own default -- prompt for
// anything not obviously safe, rather than New silently widening it) paired
// with AutoDenyPolicy for the can_use_tool control requests that still
// arrive under it. No UI is wired up yet to make an informed allow/deny
// call, and a wrongly-approved tool use (e.g. an errant Bash command) is far
// more costly than a wrongly-denied one, which just surfaces to the caller
// as a denial the agent can react to. A caller that wants a fully
// autonomous run can opt in explicitly with
// WithPermissionMode("bypassPermissions") and/or
// WithPermissionPolicy(AutoApprovePolicy{}).
func New(worktreePath string, opts ...Option) (*Client, error) {
	o := options{
		permissionMode:   "default",
		permissionPolicy: AutoDenyPolicy{},
		cliPath:          "claude",
		closeGracePeriod: defaultCloseGracePeriod,
	}
	for _, opt := range opts {
		opt(&o)
	}

	args := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--input-format", "stream-json",
		"--permission-mode", o.permissionMode,
	}

	tr, err := startTransport(worktreePath, o.cliPath, args, o.extraEnv, o.logWriter)
	if err != nil {
		return nil, err
	}
	return &Client{
		tr:               tr,
		permissionPolicy: o.permissionPolicy,
		closeGracePeriod: o.closeGracePeriod,
	}, nil
}

type wireUserTurn struct {
	Type    string          `json:"type"`
	Message wireUserContent `json:"message"`
}

type wireUserContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Prompt sends text as a new user turn and streams the CLI's response onto
// updates as each message arrives -- forwarded incrementally, not buffered
// until the turn ends -- returning once the terminal "result" message is
// received. updates is always closed before Prompt returns, including on
// error. can_use_tool control requests are handled internally via the
// client's PermissionPolicy and never appear on updates.
func (c *Client) Prompt(ctx context.Context, text string, updates chan<- Message) (ResultMessage, error) {
	defer close(updates)

	if err := c.tr.writeLine(wireUserTurn{
		Type:    "user",
		Message: wireUserContent{Role: "user", Content: text},
	}); err != nil {
		return ResultMessage{}, fmt.Errorf("claudecode: send prompt: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ResultMessage{}, ctx.Err()
		case lr, ok := <-c.tr.lines:
			if !ok {
				return ResultMessage{}, fmt.Errorf("claudecode: cli exited before sending a result message")
			}
			if lr.err != nil {
				return ResultMessage{}, fmt.Errorf("claudecode: read cli output: %w", lr.err)
			}

			parsed, err := decodeLine(lr.data)
			if err != nil || parsed == nil {
				// Malformed or unrecognized line: skip rather than fail the
				// whole turn over it.
				continue
			}

			switch v := parsed.(type) {
			case *controlRequest:
				if err := c.handleControlRequest(ctx, v); err != nil {
					return ResultMessage{}, fmt.Errorf("claudecode: respond to control request: %w", err)
				}
			case ResultMessage:
				return v, nil
			case Message:
				select {
				case updates <- v:
				case <-ctx.Done():
					return ResultMessage{}, ctx.Err()
				}
			}
		}
	}
}

type wireControlResponse struct {
	Type     string                     `json:"type"`
	Response wireControlResponsePayload `json:"response"`
}

type wireControlResponsePayload struct {
	Subtype   string         `json:"subtype"`
	RequestID string         `json:"request_id"`
	Response  map[string]any `json:"response,omitempty"`
	Error     string         `json:"error,omitempty"`
}

func (c *Client) handleControlRequest(ctx context.Context, req *controlRequest) error {
	if req.Subtype != "can_use_tool" {
		return c.tr.writeLine(wireControlResponse{
			Type: "control_response",
			Response: wireControlResponsePayload{
				Subtype:   "error",
				RequestID: req.RequestID,
				Error:     fmt.Sprintf("unsupported control request subtype %q", req.Subtype),
			},
		})
	}

	allow, updatedInput, denyMessage, err := c.permissionPolicy.Decide(ctx, CanUseToolRequest{
		ToolName:              req.ToolName,
		Input:                 req.Input,
		ToolUseID:             req.ToolUseID,
		PermissionSuggestions: req.PermissionSuggestions,
	})
	if err != nil {
		return c.tr.writeLine(wireControlResponse{
			Type: "control_response",
			Response: wireControlResponsePayload{
				Subtype:   "error",
				RequestID: req.RequestID,
				Error:     err.Error(),
			},
		})
	}

	payload := map[string]any{"behavior": "deny", "message": denyMessage}
	if allow {
		in := updatedInput
		if in == nil {
			in = req.Input
		}
		payload = map[string]any{"behavior": "allow", "updatedInput": in}
	}

	return c.tr.writeLine(wireControlResponse{
		Type: "control_response",
		Response: wireControlResponsePayload{
			Subtype:   "success",
			RequestID: req.RequestID,
			Response:  payload,
		},
	})
}

// Close closes the CLI's stdin, giving it a grace period to flush and exit
// on its own, then force-kills it if it hasn't. Safe to call more than
// once.
func (c *Client) Close() error {
	return c.tr.close(c.closeGracePeriod)
}
