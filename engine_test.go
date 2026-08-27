package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCLIEnvOpts returns options pointing at the fake CLI for scenario,
// with extra env vars layered in (withExtraEnv replaces the environment
// wholesale, so the scenario var must be set here too).
func fakeCLIEnvOpts(t *testing.T, scenario string, extra ...string) []Option {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	env := append(os.Environ(),
		"CLAUDECODE_FAKE_CLI=1",
		"CLAUDECODE_FAKE_SCENARIO="+scenario,
	)

	return []Option{WithCLIPath(self), withExtraEnv(append(env, extra...))}
}

// scriptablePolicy lets a test decide can_use_tool outcomes per tool_use_id
// and records every request it saw.
type scriptablePolicy struct {
	mu     sync.Mutex
	got    []CanUseToolRequest
	decide func(ctx context.Context, req CanUseToolRequest) (bool, map[string]any, string, []map[string]any, bool, error)
}

func (p *scriptablePolicy) Decide(ctx context.Context, req CanUseToolRequest) (bool, map[string]any, string, []map[string]any, bool, error) {
	p.mu.Lock()
	p.got = append(p.got, req)
	p.mu.Unlock()

	return p.decide(ctx, req)
}

func (p *scriptablePolicy) requests() []CanUseToolRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]CanUseToolRequest(nil), p.got...)
}

// blockingPolicy blocks Decide until its context is cancelled, proving the
// control_cancel_request path end to end.
type blockingPolicy struct{}

func (blockingPolicy) Decide(ctx context.Context, _ CanUseToolRequest) (bool, map[string]any, string, []map[string]any, bool, error) {
	<-ctx.Done()
	return false, nil, "", nil, false, ctx.Err()
}

func TestClient_SequentialPromptsReuseSubprocess(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "two_turns")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	for turn := 1; turn <= 2; turn++ {
		updates := make(chan Message)
		drained := make(chan struct{})

		go func() {
			for msg := range updates {
				_ = msg // drain to unblock Prompt's forwarding goroutine
			}

			close(drained)
		}()

		res, err := c.Prompt(context.Background(), "go", updates)
		if err != nil {
			t.Fatalf("Prompt() turn %d error = %v", turn, err)
		}

		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.Fatalf("turn %d: updates not drained", turn)
		}

		want := "done-1"
		if turn == 2 {
			want = "done-2"
		}

		if res.Result != want {
			t.Fatalf("Prompt() turn %d result = %q, want %q", turn, res.Result, want)
		}
	}

	if c.tr.exited() {
		t.Fatal("subprocess exited between turns; want one persistent process")
	}
}

func TestClient_InterruptMidTurn(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "interruptible")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)
	resultCh := make(chan promptResult, 1)

	go func() {
		res, err := c.Prompt(context.Background(), "go", updates)
		resultCh <- promptResult{res, err}
	}()

	select {
	case msg, ok := <-updates:
		if !ok {
			t.Fatal("updates closed before first message")
		}

		if _, isAssistant := msg.(AssistantMessage); !isAssistant {
			t.Fatalf("update = %#v, want AssistantMessage", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first streamed message")
	}

	if err := c.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("Prompt() error = %v", r.err)
		}

		if r.res.Result != "interrupted" {
			t.Fatalf("Prompt() result = %q, want %q", r.res.Result, "interrupted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Prompt() to return after interrupt")
	}
}

func TestClient_ConcurrentControlRequestsCorrelateOutOfOrder(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "reorder")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	errCh := make(chan error, 2)
	go func() { errCh <- c.SetPermissionMode(context.Background(), "acceptEdits") }()
	go func() {
		m := "sonnet"
		errCh <- c.SetModel(context.Background(), &m)
	}()

	for range 2 {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("concurrent control request error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a concurrent control request")
		}
	}
}

func TestClient_CloseDuringPromptUnblocks(t *testing.T) {
	t.Parallel()

	opts := append(fakeCLIOptions(t, "hang"), withCloseGracePeriod(200*time.Millisecond))

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	updates := make(chan Message)
	promptDone := make(chan error, 1)

	go func() {
		_, err := c.Prompt(context.Background(), "hi", updates)
		promptDone <- err
	}()
	go func() {
		for msg := range updates {
			_ = msg // drain to unblock Prompt's forwarding goroutine
		}
	}()

	// Fake "hang" CLI dies at the SIGTERM stage -- a signal we sent
	// ourselves, so the wait error is suppressed.
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil (post-SIGTERM wait error suppressed)", err)
	}

	select {
	case err := <-promptDone:
		if err == nil {
			t.Fatal("Prompt() returned nil error after Close(), want an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt() hung after Close()")
	}
}

func TestClient_UnknownControlResponseIgnored(t *testing.T) {
	t.Parallel()

	// The control_echo scenario opens by sending a control_response for a
	// request the client never made; the Interrupt below must still succeed.
	opts := fakeCLIEnvOpts(t, "control_echo", "CLAUDECODE_FAKE_RESPONSES={}")

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt() after stray control_response error = %v", err)
	}
}

func TestClient_CrashForceResolvesPendingRequests(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "crash")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)
	promptDone := make(chan error, 1)

	go func() {
		_, err := c.Prompt(context.Background(), "hi", updates)
		promptDone <- err
	}()
	go func() {
		for msg := range updates {
			_ = msg // drain to unblock Prompt's forwarding goroutine
		}
	}()

	ctrlDone := make(chan error, 1)
	go func() { ctrlDone <- c.SetPermissionMode(context.Background(), "acceptEdits") }()

	for range 2 {
		select {
		case err := <-promptDone:
			if err == nil {
				t.Fatal("Prompt() returned nil error on crash, want an error")
			}
		case err := <-ctrlDone:
			if err == nil {
				t.Fatal("pending control request returned nil error on crash, want an error")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for crash force-resolution")
		}
	}
}

func TestClient_OutboundControlWireShapes(t *testing.T) {
	t.Parallel()

	const numRequests = 10

	opts := fakeCLIEnvOpts(t, "capture_stdin",
		"CLAUDECODE_FAKE_MAX_LINES="+strconv.Itoa(numRequests))

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx := context.Background()
	sonnet := "sonnet"

	if err := c.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}

	if err := c.SetPermissionMode(ctx, "acceptEdits"); err != nil {
		t.Fatalf("SetPermissionMode() error = %v", err)
	}

	if err := c.SetModel(ctx, &sonnet); err != nil {
		t.Fatalf("SetModel(string) error = %v", err)
	}

	if err := c.SetModel(ctx, nil); err != nil {
		t.Fatalf("SetModel(nil) error = %v", err)
	}

	if err := c.RewindFiles(ctx, "msg-1"); err != nil {
		t.Fatalf("RewindFiles() error = %v", err)
	}

	if _, err := c.GetMCPStatus(ctx); err != nil {
		t.Fatalf("GetMCPStatus() error = %v", err)
	}

	if _, err := c.GetContextUsage(ctx); err != nil {
		t.Fatalf("GetContextUsage() error = %v", err)
	}

	if err := c.ReconnectMCPServer(ctx, "srv"); err != nil {
		t.Fatalf("ReconnectMCPServer() error = %v", err)
	}

	if err := c.ToggleMCPServer(ctx, "srv", false); err != nil {
		t.Fatalf("ToggleMCPServer() error = %v", err)
	}

	if err := c.StopTask(ctx, "task-1"); err != nil {
		t.Fatalf("StopTask() error = %v", err)
	}

	// The fake CLI reports the recorded stdin lines via the result message
	// once it has answered all numRequests control requests.
	var captured []string

	for msg := range c.ReceiveResponse(ctx) {
		if res, ok := msg.(ResultMessage); ok {
			if err := json.Unmarshal([]byte(res.Result), &captured); err != nil {
				t.Fatalf("unmarshal captured stdin: %v", err)
			}

			break
		}
	}

	if len(captured) != numRequests {
		t.Fatalf("captured %d stdin lines, want %d: %q", len(captured), numRequests, captured)
	}

	wantSubtypes := []string{
		"interrupt", "set_permission_mode", "set_model", "set_model",
		"rewind_files", "mcp_status", "get_context_usage", "mcp_reconnect",
		"mcp_toggle", "stop_task",
	}
	seenIDs := map[string]bool{}

	for i, line := range captured {
		var env struct {
			Type      string         `json:"type"`
			RequestID string         `json:"request_id"`
			Request   map[string]any `json:"request"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("line %d not valid JSON: %q", i, line)
		}

		if env.Type != "control_request" {
			t.Fatalf("line %d type = %q, want control_request", i, env.Type)
		}

		if env.RequestID == "" || seenIDs[env.RequestID] {
			t.Fatalf("line %d request_id %q missing or duplicated", i, env.RequestID)
		}

		seenIDs[env.RequestID] = true

		sub, _ := env.Request["subtype"].(string)
		if sub != wantSubtypes[i] {
			t.Fatalf("line %d subtype = %q, want %q", i, sub, wantSubtypes[i])
		}

		var want map[string]any

		switch wantSubtypes[i] {
		case "set_permission_mode":
			want = map[string]any{"mode": "acceptEdits"}
		case "rewind_files":
			want = map[string]any{"user_message_id": "msg-1"}
		case "mcp_reconnect":
			want = map[string]any{"serverName": "srv"}
		case "mcp_toggle":
			want = map[string]any{"serverName": "srv", "enabled": false}
		case "stop_task":
			want = map[string]any{"task_id": "task-1"}
		}

		for k, v := range want {
			got, ok := env.Request[k]
			if !ok || got != v {
				t.Fatalf("line %d request[%q] = %#v, want %#v", i, k, got, v)
			}
		}
	}

	// set_model's model field: string then JSON null.
	var withModel struct {
		Request struct {
			Model *string `json:"model"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(captured[2]), &withModel); err != nil {
		t.Fatalf("unmarshal set_model line: %v", err)
	}

	if withModel.Request.Model == nil || *withModel.Request.Model != "sonnet" {
		t.Fatalf("set_model line model = %#v, want sonnet", withModel.Request.Model)
	}

	if err := json.Unmarshal([]byte(captured[3]), &withModel); err != nil {
		t.Fatalf("unmarshal set_model(nil) line: %v", err)
	}

	if withModel.Request.Model != nil {
		t.Fatalf("set_model(nil) line model = %#v, want JSON null", withModel.Request.Model)
	}
}

func TestClient_OutboundControlErrorResponse(t *testing.T) {
	t.Parallel()

	opts := fakeCLIEnvOpts(t, "control_echo",
		`CLAUDECODE_FAKE_RESPONSES={"set_permission_mode":{"__error":"mode rejected"}}`)

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	err = c.SetPermissionMode(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "mode rejected") {
		t.Fatalf("SetPermissionMode() error = %v, want one containing %q", err, "mode rejected")
	}
}

func TestClient_OutboundControlTimeout(t *testing.T) {
	t.Parallel()

	opts := fakeCLIEnvOpts(t, "control_echo",
		"CLAUDECODE_FAKE_RESPONSES={}",
		"CLAUDECODE_FAKE_IGNORE=interrupt")

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	c.controlTimeout = 200 * time.Millisecond

	if err := c.Interrupt(context.Background()); err == nil {
		t.Fatal("Interrupt() with no response = nil error, want timeout error")
	}

	c.pendingMu.Lock()
	left := len(c.pending)
	c.pendingMu.Unlock()

	if left != 0 {
		t.Fatalf("pending map holds %d entries after timeout, want 0", left)
	}
}

func TestClient_MCPStatusAndContextUsageParse(t *testing.T) {
	t.Parallel()

	responses := `{"mcp_status":{"mcpServers":[{"name":"srv","status":"connected","` +
		`serverInfo":{"name":"srv","version":"1"},"tools":[{"name":"t","description":"d"}]}]},` +
		`"get_context_usage":{"categories":[{"name":"tools","tokens":10,"color":"blue"}],` +
		`"totalTokens":100,"maxTokens":200,"percentage":50,"model":"m","memoryFiles":3}}`
	opts := fakeCLIEnvOpts(t, "control_echo", "CLAUDECODE_FAKE_RESPONSES="+responses)

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx := context.Background()

	status, err := c.GetMCPStatus(ctx)
	if err != nil {
		t.Fatalf("GetMCPStatus() error = %v", err)
	}

	if len(status.MCPServers) != 1 || status.MCPServers[0].Name != "srv" ||
		status.MCPServers[0].ServerInfo == nil || status.MCPServers[0].ServerInfo.Version != "1" ||
		len(status.MCPServers[0].Tools) != 1 || status.MCPServers[0].Tools[0].Name != "t" {
		t.Fatalf("GetMCPStatus() = %#v, want one parsed server with info and tools", status)
	}

	usage, err := c.GetContextUsage(ctx)
	if err != nil {
		t.Fatalf("GetContextUsage() error = %v", err)
	}

	if usage.TotalTokens != 100 || usage.MaxTokens != 200 || usage.Percentage != 50 ||
		usage.Model != "m" || len(usage.Categories) != 1 || usage.Categories[0].Tokens != 10 {
		t.Fatalf("GetContextUsage() typed fields = %#v", usage)
	}

	if !strings.Contains(string(usage.Raw), `"memoryFiles":3`) {
		t.Fatalf("GetContextUsage().Raw missing unmodeled fields: %s", usage.Raw)
	}
}

func TestClient_CanUseToolRoundTripFields(t *testing.T) {
	t.Parallel()

	policy := &scriptablePolicy{decide: func(_ context.Context, req CanUseToolRequest) (bool, map[string]any, string, []map[string]any, bool, error) {
		switch req.ToolUseID {
		case "req-a": // allow with updatedPermissions
			return true, req.Input, "", []map[string]any{{"permission": "test"}}, false, nil
		case "req-b": // allow without updatedPermissions
			return true, req.Input, "", nil, false, nil
		default: // deny with interrupt
			return false, nil, "no", nil, true, nil
		}
	}}

	c, err := New(t.TempDir(), append(fakeCLIOptions(t, "policy_roundtrip"), WithPermissionPolicy(policy))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)

	res, err := c.Prompt(context.Background(), "go", updates)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var resps []map[string]any
	if err := json.Unmarshal([]byte(res.Result), &resps); err != nil {
		t.Fatalf("unmarshal responses: %v", err)
	}

	if len(resps) != 3 {
		t.Fatalf("got %d control responses, want 3", len(resps))
	}

	// Enriched request fields must have reached the policy.
	reqs := policy.requests()
	if len(reqs) != 3 {
		t.Fatalf("policy saw %d requests, want 3", len(reqs))
	}

	first := reqs[0]
	if first.Title != "t" || first.DisplayName != "dn" || first.Description != "d" ||
		first.DecisionReason != "dr" || first.BlockedPath != "/tmp/x" || first.AgentID != "agent-1" {
		t.Fatalf("enriched fields not passed through: %#v", first)
	}

	assertResponse := func(i int, wantKeys map[string]any, absentKeys ...string) {
		t.Helper()

		inner, _ := resps[i]["response"].(map[string]any)
		if inner == nil {
			t.Fatalf("response %d has no response object: %#v", i, resps[i])
		}

		for k, v := range wantKeys {
			if inner[k] != v {
				t.Fatalf("response %d [%q] = %#v, want %#v", i, k, inner[k], v)
			}
		}

		for _, k := range absentKeys {
			if _, present := inner[k]; present {
				t.Fatalf("response %d unexpectedly contains %q: %#v", i, k, inner)
			}
		}
	}

	assertResponse(0, map[string]any{"behavior": "allow"})
	assertResponse(1, map[string]any{"behavior": "allow"}, "updatedPermissions")
	assertResponse(2, map[string]any{"behavior": "deny", "message": "no"}, "updatedPermissions")

	// updatedPermissions only on response 0; interrupt only on the deny.
	inner0, _ := resps[0]["response"].(map[string]any)

	perms, ok := inner0["updatedPermissions"].([]any)
	if !ok || len(perms) != 1 {
		t.Fatalf("response 0 updatedPermissions = %#v, want one entry", inner0["updatedPermissions"])
	}

	inner2, _ := resps[2]["response"].(map[string]any)
	if inner2["interrupt"] != true {
		t.Fatalf("response 2 interrupt = %#v, want true", inner2["interrupt"])
	}
}

func TestClient_HookCallbackDispatch(t *testing.T) {
	t.Parallel()

	const hookID = "hook_test_1"

	opts := fakeCLIEnvOpts(t, "hook_dispatch", "CLAUDECODE_FAKE_HOOK_ID="+hookID)

	c, err := New(t.TempDir(), append(opts, WithPermissionPolicy(blockingPolicy{}))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	c.hookMu.Lock()
	c.hookCallbacks[hookID] = func(_ context.Context, input map[string]any, toolUseID string) (*HookJSONOutput, error) {
		return &HookJSONOutput{
			Decision:           "block",
			HookSpecificOutput: map[string]any{"sawInput": input["a"], "tool": toolUseID},
		}, nil
	}
	c.hookMu.Unlock()

	updates := make(chan Message)

	res, err := c.Prompt(context.Background(), "go", updates)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var out struct {
		Hook        map[string]any `json:"hook"`
		AfterCancel map[string]any `json:"after_cancel"`
	}
	if err := json.Unmarshal([]byte(res.Result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	hookResp, _ := out.Hook["response"].(map[string]any)
	if hookResp == nil {
		t.Fatalf("hook response missing response_data: %#v", out.Hook)
	}
	// The callback's output must come back verbatim as response_data, with
	// no wrapping key.
	hso, _ := hookResp["hookSpecificOutput"].(map[string]any)
	if hookResp["decision"] != "block" || hso == nil || hso["sawInput"] != float64(1) || hso["tool"] != "tool-h" {
		t.Fatalf("hook response_data = %#v, want callback output verbatim", hookResp)
	}

	// The cancelled can_use_tool handler must never have answered: the
	// first control_response after the cancel is for the req-h3 marker, and
	// no req-h2 response ever appears.
	if id, _ := out.AfterCancel["request_id"].(string); id != "req-h3" {
		t.Fatalf("first response after cancel was for %q, want req-h3 (req-h2 must stay unanswered)", id)
	}
}

func TestClient_InboundControlEdgeCases(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "control_traffic")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)

	res, err := c.Prompt(context.Background(), "go", updates)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var out struct {
		Mcp  map[string]any `json:"mcp"`
		Hook map[string]any `json:"hook"`
		Cut  map[string]any `json:"cut"`
	}
	if err := json.Unmarshal([]byte(res.Result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if sub, _ := out.Mcp["subtype"].(string); sub != "error" {
		t.Fatalf("mcp_message response subtype = %q, want error", sub)
	} else if msg, _ := out.Mcp["error"].(string); !strings.Contains(msg, "Missing server_name or message for MCP request") {
		t.Fatalf("mcp_message error = %q, want missing-fields message", msg)
	}

	if sub, _ := out.Hook["subtype"].(string); sub != "error" {
		t.Fatalf("unregistered hook response subtype = %q, want error", sub)
	} else if msg, _ := out.Hook["error"].(string); !strings.Contains(msg, "No hook callback found for ID: nope") {
		t.Fatalf("unregistered hook error = %q", msg)
	}

	if sub, _ := out.Cut["subtype"].(string); sub != "success" {
		t.Fatalf("can_use_tool response subtype = %q, want success: %#v", sub, out.Cut)
	}
}

func TestClient_HooksRegisteredAndRoundTrip(t *testing.T) {
	t.Parallel()

	var (
		gotInput     map[string]any
		gotToolUseID string
	)

	c, err := New(t.TempDir(), append(fakeCLIOptions(t, "hooks_roundtrip"),
		WithHooks(map[HookEvent][]HookMatcher{
			HookEventPreToolUse: {{
				Matcher: "Bash",
				Hooks: []HookCallback{func(_ context.Context, input map[string]any, toolUseID string) (*HookJSONOutput, error) {
					gotInput, gotToolUseID = input, toolUseID
					cont := false

					return &HookJSONOutput{
						Continue:           &cont,
						Decision:           "approve",
						HookSpecificOutput: map[string]any{"permissionDecision": "allow", "updatedInput": map[string]any{"command": "true"}},
					}, nil
				}},
				Timeout: 30 * time.Second,
			}},
		}))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)

	res, err := c.Prompt(context.Background(), "go", updates)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var out struct {
		InitializeHooks map[string][]map[string]any `json:"initialize_hooks"`
		Hook            map[string]any              `json:"hook"`
	}
	if err := json.Unmarshal([]byte(res.Result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// (b) the initialize request's hooks payload carries the matcher,
	// minted callback ID, and timeout.
	matchers := out.InitializeHooks["PreToolUse"]
	if len(matchers) != 1 {
		t.Fatalf("initialize hooks for PreToolUse = %#v, want one matcher entry", matchers)
	}

	if matchers[0]["matcher"] != "Bash" {
		t.Fatalf("matcher = %#v, want Bash", matchers[0]["matcher"])
	}

	if matchers[0]["timeout"] != float64(30) {
		t.Fatalf("timeout = %#v, want 30", matchers[0]["timeout"])
	}

	ids, _ := matchers[0]["hookCallbackIds"].([]any)
	if len(ids) != 1 || ids[0] != "hook_0" {
		t.Fatalf("hookCallbackIds = %#v, want [hook_0]", matchers[0]["hookCallbackIds"])
	}

	// (a) the callback received a PreToolUse-shaped input and its
	// *HookJSONOutput round-tripped into the control_response.
	in, err := DecodeHookInput[PreToolUseHookInput](gotInput)
	if err != nil {
		t.Fatalf("DecodeHookInput: %v", err)
	}

	if in.HookEventName != "PreToolUse" || in.ToolName != "Bash" ||
		in.SessionID != "sess-1" || in.ToolInput["command"] != "ls" {
		t.Fatalf("decoded hook input = %#v, want PreToolUseHookInput fields", in)
	}

	if gotToolUseID != "tool-1" {
		t.Fatalf("toolUseID = %q, want tool-1", gotToolUseID)
	}

	hookResp, _ := out.Hook["response"].(map[string]any)
	if hookResp == nil {
		t.Fatalf("hook response missing response_data: %#v", out.Hook)
	}

	if cont, _ := hookResp["continue"].(bool); cont {
		t.Fatalf("continue = %#v, want false", hookResp["continue"])
	}

	if hookResp["decision"] != "approve" {
		t.Fatalf("decision = %#v, want approve", hookResp["decision"])
	}

	hso, _ := hookResp["hookSpecificOutput"].(map[string]any)
	if hso == nil || hso["permissionDecision"] != "allow" {
		t.Fatalf("hookSpecificOutput = %#v, want permissionDecision allow", hookResp["hookSpecificOutput"])
	}

	if up, _ := hso["updatedInput"].(map[string]any); up == nil || up["command"] != "true" {
		t.Fatalf("hookSpecificOutput.updatedInput = %#v, want command true", hso["updatedInput"])
	}
}

func TestClient_HooksSameEventDispatchedConcurrently(t *testing.T) {
	t.Parallel()

	entered := make(chan string, 2)
	release := make(chan struct{})
	newCB := func(id string) HookCallback {
		return func(ctx context.Context, _ map[string]any, _ string) (*HookJSONOutput, error) {
			entered <- id

			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			out := &HookJSONOutput{Decision: "done-" + id}

			return out, nil
		}
	}

	c, err := New(t.TempDir(), append(fakeCLIOptions(t, "hooks_concurrent"),
		WithHooks(map[HookEvent][]HookMatcher{
			HookEventPostToolUse: {
				{Matcher: "Bash", Hooks: []HookCallback{newCB("a")}},
				{Matcher: "Write", Hooks: []HookCallback{newCB("b")}},
			},
		}))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)
	promptDone := make(chan promptResult, 1)

	go func() {
		res, err := c.Prompt(context.Background(), "go", updates)
		promptDone <- promptResult{res, err}
	}()
	go func() {
		for msg := range updates {
			_ = msg // drain to unblock Prompt's forwarding goroutine
		}
	}()

	// Both callbacks must be in flight simultaneously -- this blocks until
	// both report entry (or the test times out, which is exactly the
	// failure mode serialized dispatch would produce).
	seen := map[string]bool{}

	for i := range 2 {
		select {
		case id := <-entered:
			seen[id] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d hook callbacks entered after 5s (seen %v), want 2 concurrently", i+1, seen)
		}
	}

	close(release)

	select {
	case r := <-promptDone:
		if r.err != nil {
			t.Fatalf("Prompt() error = %v", r.err)
		}

		var out struct {
			Responses []map[string]any `json:"responses"`
		}
		if err := json.Unmarshal([]byte(r.res.Result), &out); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}

		if len(out.Responses) != 2 {
			t.Fatalf("got %d hook responses, want 2", len(out.Responses))
		}

		decisions := map[string]bool{}

		for _, r := range out.Responses {
			inner, _ := r["response"].(map[string]any)
			if inner == nil {
				t.Fatalf("hook response missing response_data: %#v", r)
			}

			if d, _ := inner["decision"].(string); d != "" {
				decisions[d] = true
			}
		}

		if !decisions["done-a"] || !decisions["done-b"] {
			t.Fatalf("hook response decisions = %v, want both done-a and done-b", decisions)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Prompt() after releasing hooks")
	}
}
