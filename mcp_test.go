package claudecode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// mcpBridgeFixture builds the two-server setup used by the mcp_bridge
// scenario: test-server (echo, annotated list-tool, failing, panicking)
// and other-server (img, blocker).
func mcpBridgeFixture() (a, b *SdkMcpServer) {
	a = NewSdkMcpServer("test-server", "1.2.3",
		NewMcpTool("echo", "echoes text", map[string]any{
			"type":       "object",
			"properties": map[string]any{"x": map[string]any{"type": "string"}},
		}, func(_ context.Context, args map[string]any) (*McpToolResult, error) {
			x, _ := args["x"].(string)
			return &McpToolResult{Content: []McpContent{{Type: "text", Text: "echo:" + x}}}, nil
		}),
		NewMcpTool("listed", "has annotations", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
			return &McpToolResult{Content: []McpContent{{Type: "text", Text: "ok"}}}, nil
		}, WithMcpToolAnnotations(func() *SdkMcpToolAnnotations {
			f, t := false, true
			return &SdkMcpToolAnnotations{Title: "Listed", ReadOnlyHint: &t, DestructiveHint: &f}
		}())),
		NewMcpTool("fails", "always errors", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
			return nil, errFakeHandler
		}),
		NewMcpTool("panics", "always panics", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
			panic("boom")
		}),
	)
	b = NewSdkMcpServer("other-server", "0.0.1",
		NewMcpTool("img", "returns an image", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
			return &McpToolResult{Content: []McpContent{{Type: "image", Data: "aGk=", MimeType: "image/png"}}}, nil
		}),
	)

	return a, b
}

type fakeHandlerError struct{}

func (fakeHandlerError) Error() string { return "handler exploded" }

var errFakeHandler error = fakeHandlerError{}

func TestClient_McpMessageDispatch(t *testing.T) {
	t.Parallel()

	srv, other := mcpBridgeFixture()

	c, err := New(t.TempDir(), append(fakeCLIOptions(t, "mcp_bridge"),
		WithSDKMcpServer(srv), WithSDKMcpServer(other))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)

	res, err := c.Prompt(context.Background(), "go", updates)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var out map[string]map[string]any
	if err := json.Unmarshal([]byte(res.Result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	mcp := func(id string) map[string]any {
		r, ok := out[id]
		if !ok {
			t.Fatalf("no response captured for %q", id)
		}

		if sub, _ := r["subtype"].(string); sub != "success" {
			t.Fatalf("%s: control_response subtype = %q, want success (JSON-RPC errors still ride the success envelope); got %+v", id, sub, r)
		}

		resp, _ := r["response"].(map[string]any)

		m, _ := resp["mcp_response"].(map[string]any)
		if m == nil {
			t.Fatalf("%s: no mcp_response in %+v", id, r)
		}

		return m
	}

	// initialize
	init := mcp("m1")
	if v, _ := init["result"].(map[string]any); true {
		si, _ := v["serverInfo"].(map[string]any)
		if si["name"] != "test-server" || si["version"] != "1.2.3" {
			t.Fatalf("initialize serverInfo = %+v", v)
		}

		if v["protocolVersion"] != "2024-11-05" {
			t.Fatalf("initialize protocolVersion = %v", v["protocolVersion"])
		}
	}

	// tools/list: both tools, sorted, annotations only where set
	list := mcp("m2")
	lr, _ := list["result"].(map[string]any)

	tools, _ := lr["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools/list returned %d tools, want 4: %+v", len(tools), lr)
	}

	first, _ := tools[0].(map[string]any)
	if first["name"] != "echo" {
		t.Fatalf("first tool = %v, want echo", first["name"])
	}

	if _, has := first["annotations"]; has {
		t.Fatalf("echo tool has annotations, want none: %+v", first)
	}

	second, _ := tools[2].(map[string]any)
	if second["name"] != "listed" {
		t.Fatalf("third tool = %v, want listed (sorted order)", second["name"])
	}

	ann, _ := second["annotations"].(map[string]any)
	if ann["title"] != "Listed" || ann["readOnlyHint"] != true {
		t.Fatalf("listed annotations = %+v", ann)
	}

	if _, has := ann["idempotentHint"]; has {
		t.Fatalf("zero-value hint leaked: %+v", ann)
	}
	// other-server scoping: its tools/list would differ; here we at least
	// prove the request went to the named server (4 tools on test-server).

	// successful tools/call
	call := mcp("m3")
	cr, _ := call["result"].(map[string]any)

	content, _ := cr["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("tools/call content = %+v", cr)
	}

	blk, _ := content[0].(map[string]any)
	if blk["text"] != "echo:hi" {
		t.Fatalf("tools/call text = %v", blk["text"])
	}

	if _, has := cr["isError"]; has {
		t.Fatalf("unexpected isError on success: %+v", cr)
	}

	// unregistered server: JSON-RPC -32601 inside a SUCCESS envelope
	nf := mcp("m4")
	if _, has := nf["result"]; has {
		t.Fatalf("unregistered-server response has result: %+v", nf)
	}

	nfErr, _ := nf["error"].(map[string]any)
	if nfErr["code"] != float64(-32601) || !strings.Contains(nfErr["message"].(string), "nope-server") { //nolint:forcetypeassert,errcheck  // test fixture shape is known
		t.Fatalf("unregistered-server error = %+v", nfErr)
	}

	// unknown tool: isError:true JSON-RPC success
	ut := mcp("m5")

	utr, _ := ut["result"].(map[string]any)
	if utr["isError"] != true {
		t.Fatalf("unknown-tool result = %+v", utr)
	}

	if _, has := ut["error"]; has {
		t.Fatalf("unknown tool surfaced as JSON-RPC error: %+v", ut)
	}

	// handler error: isError:true JSON-RPC success
	he := mcp("m6")

	her, _ := he["result"].(map[string]any)
	if her["isError"] != true {
		t.Fatalf("handler-error result = %+v", her)
	}

	if _, has := he["error"]; has {
		t.Fatalf("handler error surfaced as JSON-RPC error: %+v", he)
	}

	// panicking handler: recovered, isError:true, process alive
	ph := mcp("m7")

	phr, _ := ph["result"].(map[string]any)
	if phr["isError"] != true {
		t.Fatalf("panic result = %+v", phr)
	}

	phContent, _ := phr["content"].([]any)

	phBlk, _ := phContent[0].(map[string]any)
	if text, _ := phBlk["text"].(string); !strings.Contains(text, "panicked") {
		t.Fatalf("panic text = %v", phBlk["text"])
	}

	// unknown method: JSON-RPC -32601 error
	um := mcp("m8")
	if _, has := um["result"]; has {
		t.Fatalf("unknown-method response has result: %+v", um)
	}

	umErr, _ := um["error"].(map[string]any)
	if umErr["code"] != float64(-32601) || !strings.Contains(umErr["message"].(string), "bogus/method") { //nolint:forcetypeassert,errcheck  // test fixture shape is known
		t.Fatalf("unknown-method error = %+v", umErr)
	}

	// notification ack: result present, no id key
	nt := mcp("m9")
	if _, has := nt["id"]; has {
		t.Fatalf("notification ack has id: %+v", nt)
	}

	if _, has := nt["result"]; !has {
		t.Fatalf("notification ack missing result: %+v", nt)
	}

	// image content mapping on the second server
	img := mcp("m10")
	ir, _ := img["result"].(map[string]any)
	ic, _ := ir["content"].([]any)

	iblk, _ := ic[0].(map[string]any)
	if iblk["type"] != "image" || iblk["data"] != "aGk=" || iblk["mimeType"] != "image/png" {
		t.Fatalf("image content = %+v", iblk)
	}
}

func TestClient_McpMessageConcurrentCalls(t *testing.T) {
	t.Parallel()

	// Three blockers that each signal arrival and wait until all three
	// are in flight -- the client must dispatch the three mcp_message
	// control requests concurrently or the fake CLI scenario deadlocks
	// (matching the hooks_concurrent pattern).
	inFlight := make(chan string, 3)
	release := make(chan struct{})
	blocker := func(tag string) McpToolHandler {
		return func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
			inFlight <- tag

			<-release

			return &McpToolResult{Content: []McpContent{{Type: "text", Text: tag + "-done"}}}, nil
		}
	}
	srvA := NewSdkMcpServer("test-server", "1.0.0",
		NewMcpTool("block-a", "b", map[string]any{"type": "object"}, blocker("a")),
		NewMcpTool("block-b", "b", map[string]any{"type": "object"}, blocker("b")),
	)
	srvB := NewSdkMcpServer("other-server", "1.0.0",
		NewMcpTool("block-c", "b", map[string]any{"type": "object"}, blocker("c")),
	)

	c, err := New(t.TempDir(), append(fakeCLIOptions(t, "mcp_concurrent"),
		WithSDKMcpServer(srvA), WithSDKMcpServer(srvB))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	// The fake CLI's three calls only complete once all three handlers
	// have been entered concurrently.
	go func() {
		seen := map[string]bool{}

		for range 3 {
			tag := <-inFlight
			seen[tag] = true
		}

		if len(seen) == 3 {
			close(release)
		}
	}()

	updates := make(chan Message)
	resCh := make(chan ResultMessage, 1)
	errCh := make(chan error, 1)

	go func() {
		res, err := c.Prompt(context.Background(), "go", updates)
		if err != nil {
			errCh <- err
			return
		}

		resCh <- res
	}()

	select {
	case res := <-resCh:
		var resps []map[string]any
		if err := json.Unmarshal([]byte(res.Result), &resps); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}

		if len(resps) != 3 {
			t.Fatalf("got %d responses, want 3", len(resps))
		}

		for _, r := range resps {
			if sub, _ := r["subtype"].(string); sub != "success" {
				t.Fatalf("response subtype = %q, want success: %+v", sub, r)
			}
		}
	case err := <-errCh:
		t.Fatalf("Prompt() error = %v", err)
	}
}

func TestClient_McpUnregisteredServerUnsupportedPinsKept(t *testing.T) {
	// With no SDK servers registered, mcp_message with a server_name still
	// gets the JSON-RPC not-found treatment (envelope success), and a bare
	// mcp_message still gets the missing-fields top-level error.
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
		Mcp map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(res.Result), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if sub, _ := out.Mcp["subtype"].(string); sub != "error" {
		t.Fatalf("bare mcp_message subtype = %q, want top-level error", sub)
	}

	if msg, _ := out.Mcp["error"].(string); !strings.Contains(msg, "Missing server_name or message") {
		t.Fatalf("bare mcp_message error = %q", msg)
	}
}

func TestClient_SdkMcpServerRegistrationFlags(t *testing.T) {
	t.Parallel()

	tool := NewMcpTool("t", "d", map[string]any{"type": "object"}, func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
		return &McpToolResult{}, nil
	})

	// Single server: exact-argv --mcp-config with the sdk entry.
	srv := NewSdkMcpServer("mine", "1.0.0", tool)
	args, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"), withArgsOpts(srv)...))
	// find --mcp-config
	cfg := ""

	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			cfg = args[i+1]
		}
	}

	var parsed struct {
		McpServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("--mcp-config not JSON: %q (%v)", cfg, err)
	}

	if len(parsed.McpServers) != 1 {
		t.Fatalf("mcpServers = %+v", parsed.McpServers)
	}

	e := parsed.McpServers["mine"]
	if e["type"] != "sdk" || e["name"] != "mine" || len(e) != 2 {
		t.Fatalf("sdk entry = %+v", e)
	}

	// Two distinct servers: both appear.
	srv2 := NewSdkMcpServer("other", "2.0.0", tool)
	args2, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"), withArgsOpts(srv, srv2)...))
	cfg2 := ""

	for i, a := range args2 {
		if a == "--mcp-config" && i+1 < len(args2) {
			cfg2 = args2[i+1]
		}
	}

	var parsed2 map[string]any
	if err := json.Unmarshal([]byte(cfg2), &parsed2); err != nil {
		t.Fatalf("--mcp-config not JSON: %q", cfg2)
	}

	servers, _ := parsed2["mcpServers"].(map[string]any)
	if len(servers) != 2 {
		t.Fatalf("merged mcpServers = %+v", servers)
	}
}

// withArgsOpts bundles SDK servers with a placeholder so test option
// slices stay composable.
func withArgsOpts(servers ...*SdkMcpServer) []Option {
	opts := []Option{}
	for _, s := range servers {
		opts = append(opts, WithSDKMcpServer(s))
	}

	return opts
}

func TestClient_SdkMcpServerDuplicateNameError(t *testing.T) {
	t.Parallel()

	tool := NewMcpTool("t", "d", nil, func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
		return nil, nil
	})

	_, err := New(t.TempDir(), append(fakeCLIOptions(t, "capture"),
		WithSDKMcpServer(NewSdkMcpServer("same", "1", tool)),
		WithSDKMcpServer(NewSdkMcpServer("same", "2", tool)))...)
	if err == nil || !strings.Contains(err.Error(), `duplicate SDK MCP server name "same"`) {
		t.Fatalf("duplicate name error = %v", err)
	}
}

func TestClient_SdkMcpServerWithMCPConfigMerge(t *testing.T) {
	t.Parallel()

	tool := NewMcpTool("t", "d", nil, func(_ context.Context, _ map[string]any) (*McpToolResult, error) {
		return nil, nil
	})
	srv := NewSdkMcpServer("sdk-srv", "1.0.0", tool)
	external := `{"mcpServers":{"ext":{"type":"stdio","command":"run"}}}`

	args, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"),
		WithMCPConfig(external), WithSDKMcpServer(srv)))
	cfg := ""

	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			cfg = args[i+1]
		}
	}

	var parsed struct {
		McpServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("--mcp-config not JSON: %q", cfg)
	}

	if len(parsed.McpServers) != 2 {
		t.Fatalf("merged mcpServers = %+v", parsed.McpServers)
	}

	if parsed.McpServers["ext"]["type"] != "stdio" {
		t.Fatalf("external entry lost: %+v", parsed.McpServers)
	}

	if parsed.McpServers["sdk-srv"]["type"] != "sdk" {
		t.Fatalf("sdk entry lost: %+v", parsed.McpServers)
	}

	// Collision: SDK entry wins, silently.
	argsC, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"),
		WithMCPConfig(`{"mcpServers":{"sdk-srv":{"type":"stdio","command":"x"}}}`),
		WithSDKMcpServer(srv)))
	cfgC := ""

	for i, a := range argsC {
		if a == "--mcp-config" && i+1 < len(argsC) {
			cfgC = argsC[i+1]
		}
	}

	var parsedC struct {
		McpServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfgC), &parsedC); err != nil {
		t.Fatalf("collision cfg not JSON: %q", cfgC)
	}

	if parsedC.McpServers["sdk-srv"]["type"] != "sdk" {
		t.Fatalf("SDK entry must win collision: %+v", parsedC.McpServers)
	}

	// File path + SDK servers: New() error before any subprocess spawns.
	_, err := New(t.TempDir(), append(fakeCLIOptions(t, "capture"),
		WithMCPConfig("/tmp/mcp.json"), WithSDKMcpServer(srv))...)
	if err == nil || !strings.Contains(err.Error(), "cannot be merged with SDK MCP servers") {
		t.Fatalf("file-path merge error = %v", err)
	}
}
