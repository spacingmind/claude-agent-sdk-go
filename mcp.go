package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// McpContent is one content block of a tool result: text
// ({"type":"text","text":...}) or image
// ({"type":"image","data":<base64>,"mimeType":...}).
type McpContent struct {
	Type     string // "text" | "image"
	Text     string
	Data     string // base64, image only
	MimeType string // image only
}

// McpToolResult is what an McpToolHandler returns. IsError marks a
// tool-level failure (surfaced as result.isError, not a JSON-RPC error).
type McpToolResult struct {
	Content []McpContent
	IsError bool
}

// McpToolHandler is the Go function behind one registered MCP tool.
type McpToolHandler func(ctx context.Context, args map[string]any) (*McpToolResult, error)

// SdkMcpToolAnnotations is what a caller declares about a tool they are
// defining. It is deliberately distinct from phase 1's McpToolAnnotations
// (the CLI's MCP-status response shape): different wire fields, different
// purpose. Pointer-backed hints distinguish "unset" from "false", and
// zero-value fields are omitted on the wire.
type SdkMcpToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// McpTool is one registered in-process tool: opaque to callers, dispatched
// by the mcp_message control-request handler.
type McpTool struct {
	name        string
	description string
	inputSchema map[string]any
	handler     McpToolHandler
	annotations *SdkMcpToolAnnotations
}

// McpToolOption customizes a tool built by NewMcpTool.
type McpToolOption func(*McpTool)

// WithMcpToolAnnotations attaches declared-behavior annotations to a tool,
// reported in tools/list responses (omitted entirely when not set).
func WithMcpToolAnnotations(ann *SdkMcpToolAnnotations) McpToolOption {
	return func(t *McpTool) { t.annotations = ann }
}

// NewMcpTool registers a tool definition: name, description, a JSON Schema
// for its arguments, and the handler the CLI's calls dispatch to.
func NewMcpTool(name, description string, inputSchema map[string]any, handler McpToolHandler, opts ...McpToolOption) *McpTool {
	t := &McpTool{
		name:        name,
		description: description,
		inputSchema: inputSchema,
		handler:     handler,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// SdkMcpServer is a named, thread-safe registry of in-process tools. The
// mutex protects the tool map against concurrent tools/call dispatches
// (each inbound control request already runs in its own goroutine).
type SdkMcpServer struct {
	name    string
	version string

	mu    sync.RWMutex
	tools map[string]*McpTool
}

// NewSdkMcpServer creates a server that WithSDKMcpServer can register on a
// Client.
func NewSdkMcpServer(name, version string, tools ...*McpTool) *SdkMcpServer {
	s := &SdkMcpServer{
		name:    name,
		version: version,
		tools:   make(map[string]*McpTool, len(tools)),
	}
	for _, t := range tools {
		if t != nil {
			s.tools[t.name] = t
		}
	}
	return s
}

// listTools returns one tools/list entry per registered tool, sorted by
// name for deterministic responses.
func (s *SdkMcpServer) listTools() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		t := s.tools[name]
		entry := map[string]any{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.inputSchema,
		}
		if t.annotations != nil {
			// Marshal/unmarshal round-trip so the struct's omitempty tags
			// are honored in the wire shape.
			raw, err := json.Marshal(t.annotations)
			if err == nil {
				var ann map[string]any
				if json.Unmarshal(raw, &ann) == nil {
					entry["annotations"] = ann
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

// callTool runs one tool handler. A panicking handler is recovered and
// converted to an error rather than crashing the read-loop's dispatch
// goroutine; an unknown tool name is an error too. Tool-level failures
// stay at this layer -- the JSON-RPC response itself still succeeds with
// isError set.
func (s *SdkMcpServer) callTool(ctx context.Context, name string, args map[string]any) (res *McpToolResult, err error) {
	s.mu.RLock()
	tool := s.tools[name]
	s.mu.RUnlock()
	if tool == nil {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	return func() (r *McpToolResult, e error) {
		defer func() {
			if p := recover(); p != nil {
				e = fmt.Errorf("tool %q panicked: %v", name, p)
			}
		}()
		return tool.handler(ctx, args)
	}()
}

// dispatchMcpMessage handles one inbound mcp_message control request's
// JSON-RPC payload and returns the mcp_response object (a JSON-RPC result
// or a JSON-RPC error object -- both ride the same control_response
// success envelope).
func (c *Client) dispatchMcpMessage(ctx context.Context, serverName string, msg map[string]any) map[string]any {
	id := msg["id"]

	server := c.sdkMcpServers[serverName]
	if server == nil {
		return jsonrpcErrorPayload(id, -32601, fmt.Sprintf("server %q not found", serverName))
	}

	method, _ := msg["method"].(string)
	switch method {
	case "initialize":
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": server.name, "version": server.version},
			},
		}

	case "tools/list":
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]any{"tools": server.listTools()},
		}

	case "tools/call":
		params, _ := msg["params"].(map[string]any)
		toolName, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]any)

		result, err := server.callTool(ctx, toolName, args)
		if err != nil {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": err.Error()}},
					"isError": true,
				},
			}
		}

		content := make([]map[string]any, 0, len(result.Content))
		for _, mc := range result.Content {
			if mc.Type == "image" {
				content = append(content, map[string]any{
					"type":     "image",
					"data":     mc.Data,
					"mimeType": mc.MimeType,
				})
			} else {
				content = append(content, map[string]any{
					"type": "text",
					"text": mc.Text,
				})
			}
		}
		res := map[string]any{"content": content}
		if result.IsError {
			res["isError"] = true
		}
		return map[string]any{"jsonrpc": "2.0", "id": id, "result": res}

	case "notifications/initialized":
		// A notification: no "id" key, but the outer control_request still
		// gets its success ack.
		return map[string]any{"jsonrpc": "2.0", "result": map[string]any{}}

	default:
		return jsonrpcErrorPayload(id, -32601, fmt.Sprintf("method %q not found", method))
	}
}

func jsonrpcErrorPayload(id any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
}

// resolveMCPConfig computes the final --mcp-config value: with no SDK
// servers registered it is o.mcpConfig passed through byte-for-byte (or
// empty); otherwise the SDK-server entries ({"type":"sdk","name":...}) are
// unioned with an inline-JSON WithMCPConfig value's mcpServers map -- SDK
// entries win on name collision, a caller mistake not worth failing over.
// A non-JSON (file-path) WithMCPConfig value cannot be merged into, so
// that combination is an error.
func resolveMCPConfig(o *options) (string, error) {
	if len(o.sdkMcpServerList) == 0 {
		return o.mcpConfig, nil
	}

	merged := map[string]any{}
	if o.mcpConfig != "" {
		var parsed struct {
			McpServers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(o.mcpConfig), &parsed); err != nil {
			return "", fmt.Errorf("claudecode: WithMCPConfig value is not inline JSON (a file path?) and cannot be merged with SDK MCP servers: %w", err)
		}
		for k, v := range parsed.McpServers {
			merged[k] = v
		}
	}
	for _, s := range o.sdkMcpServerList {
		merged[s.name] = map[string]any{"type": "sdk", "name": s.name}
	}

	out, err := json.Marshal(map[string]any{"mcpServers": merged})
	if err != nil {
		return "", fmt.Errorf("claudecode: marshal merged mcp config: %w", err)
	}
	return string(out), nil
}
