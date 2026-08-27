package claudecode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// defaultControlTimeout bounds each outbound control request's wait for its
// control_response, matching the Python SDK's request timeout.
const defaultControlTimeout = 60 * time.Second

// wireControlRequestEnvelope is the outbound control-request frame:
// {"type":"control_request","request_id":...,"request":{"subtype":...,...}}.
type wireControlRequestEnvelope struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Request   map[string]any `json:"request"`
}

type wireControlResponse struct {
	Type     string                     `json:"type"`
	Response wireControlResponsePayload `json:"response"`
}

type wireControlResponsePayload struct {
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Response  json.RawMessage `json:"response,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type pendingResult struct {
	resp *controlResponse
	err  error
}

type pendingEntry struct {
	ch chan pendingResult
}

var requestIDCounter atomic.Int64

func nextRequestID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("req_%d_%s", requestIDCounter.Add(1), hex.EncodeToString(b[:]))
}

// readLoop is the Client's single persistent reader: it decodes each NDJSON
// line, resolves outbound control requests, spawns inbound control-request
// handlers, and forwards ordinary messages onto c.msgs. It exits when the
// transport's line stream ends or the Client is closed, force-failing any
// still-pending outbound requests and closing c.msgs so stream consumers see
// channel-close rather than a hang.
func (c *Client) readLoop() {
	defer close(c.msgs)
	defer c.failAllPending(errors.New("claudecode: cli connection closed"))

	for {
		select {
		case <-c.closing:
			return
		case lr, ok := <-c.tr.lines:
			if !ok {
				return
			}
			if lr.err != nil {
				return
			}

			parsed, err := decodeLine(lr.data)
			if err != nil || parsed == nil {
				continue
			}

			switch v := parsed.(type) {
			case *controlResponse:
				c.resolvePending(v)
			case *controlRequest:
				if v.RequestID == "" {
					// Malformed (missing request_id): skip per the
					// skip-malformed-lines policy.
					continue
				}
				c.handlerWG.Add(1)
				go c.dispatchControlRequest(v)
			case *controlCancelRequest:
				c.cancelInflightHandler(v.RequestID)
			case Message:
				select {
				case c.msgs <- v:
				case <-c.closing:
					return
				}
			}
		}
	}
}

// sendControlRequest writes one outbound control request and blocks until the
// matching control_response arrives, ctx is done, the Client closes, or
// timeout elapses. A "subtype":"error" response is surfaced as a Go error.
// On any non-response termination the pending-map entry is removed so a very
// late response can't leak or land on a dead waiter.
func (c *Client) sendControlRequest(ctx context.Context, subtype string, extra map[string]any) (*controlResponse, error) {
	id := nextRequestID()
	request := map[string]any{"subtype": subtype}
	for k, v := range extra {
		request[k] = v
	}

	entry := &pendingEntry{ch: make(chan pendingResult, 1)}
	c.pendingMu.Lock()
	c.pending[id] = entry
	c.pendingMu.Unlock()

	removePending := func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}

	if err := c.tr.writeLine(wireControlRequestEnvelope{
		Type:      "control_request",
		RequestID: id,
		Request:   request,
	}); err != nil {
		removePending()
		return nil, fmt.Errorf("claudecode: send control request %q: %w", subtype, err)
	}

	timer := time.NewTimer(c.controlTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		removePending()
		return nil, ctx.Err()
	case <-c.closing:
		removePending()
		return nil, fmt.Errorf("claudecode: client closed before control request %q was answered", subtype)
	case <-timer.C:
		removePending()
		return nil, fmt.Errorf("claudecode: control request %q timed out after %s", subtype, c.controlTimeout)
	case r := <-entry.ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.resp.Subtype == "error" {
			msg := r.resp.Error
			if msg == "" {
				msg = "unknown error"
			}
			return nil, fmt.Errorf("claudecode: control request %q failed: %s", subtype, msg)
		}
		return r.resp, nil
	}
}

func (c *Client) resolvePending(resp *controlResponse) {
	c.pendingMu.Lock()
	entry := c.pending[resp.RequestID]
	delete(c.pending, resp.RequestID)
	c.pendingMu.Unlock()
	if entry == nil {
		// Unknown or already-resolved request_id: silently ignore.
		return
	}
	entry.ch <- pendingResult{resp: resp}
}

func (c *Client) failAllPending(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[string]*pendingEntry)
	c.pendingMu.Unlock()
	for _, entry := range pending {
		entry.ch <- pendingResult{err: err}
	}
}

// dispatchControlRequest runs one inbound control-request handler in its own
// goroutine with a context derived from a long-lived base (not from any
// caller's Prompt context), registered for control_cancel_request
// cancellation before handling starts.
func (c *Client) dispatchControlRequest(req *controlRequest) {
	defer c.handlerWG.Done()

	// Derived from the Client-lifetime base context (not any caller's
	// Prompt context) so handling works independent of in-flight turns,
	// and so Close cancels even handlers registered after its inflight
	// snapshot was taken.
	ctx, cancel := context.WithCancel(c.baseCtx)
	defer cancel()

	c.inflightMu.Lock()
	if _, dup := c.inflight[req.RequestID]; dup {
		c.inflightMu.Unlock()
		return
	}
	c.inflight[req.RequestID] = cancel
	c.inflightMu.Unlock()

	c.handleControlRequest(ctx, req)

	c.inflightMu.Lock()
	delete(c.inflight, req.RequestID)
	c.inflightMu.Unlock()
}

func (c *Client) cancelInflightHandler(requestID string) {
	c.inflightMu.Lock()
	cancel := c.inflight[requestID]
	c.inflightMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Client) cancelAllInflightHandlers() {
	c.inflightMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.inflight))
	for _, cancel := range c.inflight {
		cancels = append(cancels, cancel)
	}
	c.inflightMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// writeControlResponse sends one control_response for an inbound request. A
// cancelled handler writes nothing at all (the CLI cancelled the request;
// answering it would be a protocol error).
func (c *Client) writeControlResponse(requestID string, payload wireControlResponsePayload) error {
	return c.tr.writeLine(wireControlResponse{
		Type: "control_response",
		Response: wireControlResponsePayload{
			Subtype:   payload.Subtype,
			RequestID: requestID,
			Response:  payload.Response,
			Error:     payload.Error,
		},
	})
}

// handleControlRequest answers one inbound control request: can_use_tool via
// the PermissionPolicy, hook_callback via the registered callback map, and
// mcp_message via the in-process SDK MCP servers.
func (c *Client) handleControlRequest(ctx context.Context, req *controlRequest) {
	writeError := func(errStr string) {
		if ctx.Err() != nil {
			return
		}
		_ = c.writeControlResponse(req.RequestID, wireControlResponsePayload{
			Subtype: "error",
			Error:   errStr,
		})
	}

	switch req.Subtype {
	case "can_use_tool":
		allow, updatedInput, denyMessage, updatedPermissions, interrupt, err := c.permissionPolicy.Decide(ctx, CanUseToolRequest{
			ToolName:              req.ToolName,
			Input:                 req.Input,
			ToolUseID:             req.ToolUseID,
			PermissionSuggestions: req.PermissionSuggestions,
			Title:                 req.Title,
			DisplayName:           req.DisplayName,
			Description:           req.Description,
			DecisionReason:        req.DecisionReason,
			BlockedPath:           req.BlockedPath,
			AgentID:               req.AgentID,
		})
		if err != nil {
			writeError(err.Error())
			return
		}

		var payload wireControlResponsePayload
		payload.Subtype = "success"
		if allow {
			in := updatedInput
			if in == nil {
				in = req.Input
			}
			resp := map[string]any{"behavior": "allow", "updatedInput": in}
			if len(updatedPermissions) > 0 {
				resp["updatedPermissions"] = updatedPermissions
			}
			raw, _ := json.Marshal(resp)
			payload.Response = raw
		} else {
			resp := map[string]any{"behavior": "deny", "message": denyMessage}
			if interrupt {
				resp["interrupt"] = true
			}
			raw, _ := json.Marshal(resp)
			payload.Response = raw
		}

		if ctx.Err() != nil {
			return
		}
		_ = c.writeControlResponse(req.RequestID, payload)

	case "hook_callback":
		c.hookMu.Lock()
		cb := c.hookCallbacks[req.CallbackID]
		c.hookMu.Unlock()
		if cb == nil {
			writeError(fmt.Sprintf("No hook callback found for ID: %s", req.CallbackID))
			return
		}
		out, err := cb(ctx, req.Input, req.ToolUseID)
		if err != nil {
			writeError(err.Error())
			return
		}
		if ctx.Err() != nil {
			return
		}
		var raw json.RawMessage
		if out != nil {
			raw, _ = json.Marshal(out)
		}
		_ = c.writeControlResponse(req.RequestID, wireControlResponsePayload{
			Subtype:  "success",
			Response: raw,
		})

	case "mcp_message":
		if req.ServerName == "" || len(req.Message) == 0 {
			writeError("Missing server_name or message for MCP request")
			return
		}
		resp := c.dispatchMcpMessage(ctx, req.ServerName, req.Message)
		if ctx.Err() != nil {
			return
		}
		raw, _ := json.Marshal(map[string]any{"mcp_response": resp})
		_ = c.writeControlResponse(req.RequestID, wireControlResponsePayload{
			Subtype:  "success",
			Response: raw,
		})

	default:
		writeError(fmt.Sprintf("unsupported control request subtype %q", req.Subtype))
	}
}

// Query sends text as a new user turn on the default session. It does not
// wait for the CLI's reply -- pair it with ReceiveMessages/ReceiveResponse,
// or use Prompt for the one-shot convenience.
func (c *Client) Query(ctx context.Context, text string) error {
	return c.QueryWithSession(ctx, text, "")
}

// QueryWithSession sends text as a new user turn on the named session. An
// empty sessionID uses the CLI's default session.
func (c *Client) QueryWithSession(ctx context.Context, text, sessionID string) error {
	turn := wireUserTurn{
		Type:    "user",
		Message: wireUserContent{Role: "user", Content: text},
	}
	if sessionID != "" {
		turn.SessionID = sessionID
	}
	if err := c.tr.writeLine(turn); err != nil {
		return fmt.Errorf("claudecode: send prompt: %w", err)
	}
	return nil
}

// ReceiveMessages exposes the persistent message stream: every decoded
// message the CLI sends that isn't control-protocol traffic, for the rest of
// the Client's lifetime (not scoped to one turn). The channel closes when
// the CLI exits or the Client is closed.
func (c *Client) ReceiveMessages(_ context.Context) <-chan Message {
	return c.msgs
}

// ReceiveResponse forwards from ReceiveMessages and closes the returned
// channel after forwarding a ResultMessage (the result itself is forwarded
// too). It also closes when ctx is done, the Client closes, or the CLI
// exits.
func (c *Client) ReceiveResponse(ctx context.Context) <-chan Message {
	out := make(chan Message)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-c.msgs:
				if !ok {
					return
				}
				select {
				case out <- msg:
				case <-ctx.Done():
					return
				}
				if _, isResult := msg.(ResultMessage); isResult {
					return
				}
			}
		}
	}()
	return out
}

// Interrupt asks the CLI to abort the current turn.
func (c *Client) Interrupt(ctx context.Context) error {
	_, err := c.sendControlRequest(ctx, "interrupt", nil)
	return err
}

// SetPermissionMode switches the CLI's permission mode mid-session.
func (c *Client) SetPermissionMode(ctx context.Context, mode string) error {
	_, err := c.sendControlRequest(ctx, "set_permission_mode", map[string]any{"mode": mode})
	return err
}

// SetModel switches the model mid-session. A nil model resets to the
// default.
func (c *Client) SetModel(ctx context.Context, model *string) error {
	_, err := c.sendControlRequest(ctx, "set_model", map[string]any{"model": model})
	return err
}

// RewindFiles rewinds file changes back to the state at the given user
// message.
func (c *Client) RewindFiles(ctx context.Context, userMessageID string) error {
	_, err := c.sendControlRequest(ctx, "rewind_files", map[string]any{"user_message_id": userMessageID})
	return err
}

// GetMCPStatus reports the status of the CLI's configured MCP servers.
func (c *Client) GetMCPStatus(ctx context.Context) (*McpStatusResponse, error) {
	resp, err := c.sendControlRequest(ctx, "mcp_status", nil)
	if err != nil {
		return nil, err
	}
	var status McpStatusResponse
	if err := json.Unmarshal(resp.Response, &status); err != nil {
		return nil, fmt.Errorf("claudecode: parse mcp_status response: %w", err)
	}
	return &status, nil
}

// GetContextUsage reports the CLI's context-window usage breakdown.
func (c *Client) GetContextUsage(ctx context.Context) (*ContextUsageResponse, error) {
	resp, err := c.sendControlRequest(ctx, "get_context_usage", nil)
	if err != nil {
		return nil, err
	}
	var usage ContextUsageResponse
	if err := json.Unmarshal(resp.Response, &usage); err != nil {
		return nil, fmt.Errorf("claudecode: parse get_context_usage response: %w", err)
	}
	usage.Raw = append(json.RawMessage(nil), resp.Response...)
	return &usage, nil
}

// ReconnectMCPServer asks the CLI to reconnect a named MCP server. The
// camelCase serverName wire key is deliberate (it matches the CLI), not a
// porting typo.
func (c *Client) ReconnectMCPServer(ctx context.Context, serverName string) error {
	_, err := c.sendControlRequest(ctx, "mcp_reconnect", map[string]any{"serverName": serverName})
	return err
}

// ToggleMCPServer enables or disables a named MCP server.
func (c *Client) ToggleMCPServer(ctx context.Context, serverName string, enabled bool) error {
	_, err := c.sendControlRequest(ctx, "mcp_toggle", map[string]any{"serverName": serverName, "enabled": enabled})
	return err
}

// StopTask asks the CLI to stop a background task.
func (c *Client) StopTask(ctx context.Context, taskID string) error {
	_, err := c.sendControlRequest(ctx, "stop_task", map[string]any{"task_id": taskID})
	return err
}

// McpStatusResponse is the CLI's mcp_status control response.
type McpStatusResponse struct {
	MCPServers []McpServerStatus `json:"mcpServers"`
}

type McpServerStatus struct {
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	ServerInfo *McpServerInfo  `json:"serverInfo,omitempty"`
	Error      string          `json:"error,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
	Scope      string          `json:"scope,omitempty"`
	Tools      []McpToolInfo   `json:"tools,omitempty"`
}

type McpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type McpToolAnnotations struct {
	ReadOnly    bool `json:"readOnly"`
	Destructive bool `json:"destructive"`
	OpenWorld   bool `json:"openWorld"`
}

type McpToolInfo struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Annotations *McpToolAnnotations `json:"annotations"`
}

// ContextUsageResponse is the CLI's get_context_usage control response. Raw
// carries the full unmodified response JSON for fields this type doesn't
// model (memoryFiles, mcpTools, agents, gridRows, ...).
type ContextUsageResponse struct {
	Categories  []ContextUsageCategory `json:"categories"`
	TotalTokens int                    `json:"totalTokens"`
	MaxTokens   int                    `json:"maxTokens"`
	Percentage  float64                `json:"percentage"`
	Model       string                 `json:"model"`
	Raw         json.RawMessage        `json:"-"`
}

type ContextUsageCategory struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
	Color  string `json:"color"`
}
