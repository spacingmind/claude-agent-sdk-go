# SDK-MCP Bridge (SDK parity, phase 3)

Phase 3 of the SDK parity port (see `docs/plans/completed/` for phases 1
and 2). This phase adds in-process custom tool registration: a caller
defines Go functions the CLI's model can call as if they were MCP tools,
entirely in-process, with no separate MCP server process and no stdio
framing of their own — the entire exchange is tunneled as JSON-RPC 2.0
payloads inside the existing `mcp_message` control-request/response
envelope (already wired up in phase 1's engine, currently pinned to return
"unsupported control request subtype").

## Research basis

- Python reference: `_internal/sdk_mcp_bridge.py`, `_internal/query.py`'s
  `mcp_message` handling, and `__init__.py`'s `@tool`/`create_sdk_mcp_server`
  public API — read in full during phase 1's research pass.
- Community Go precedent: `github.com/severity1/claude-agent-sdk-go`
  (`mcp.go`, `internal/control/mcp.go`) — read 2026-08-27 specifically to
  resolve this phase's architecture question before writing this plan.
  **Key finding that shapes this plan**: that project implements the
  bridge with **zero external dependencies** — no MCP protocol library,
  just a hand-written `switch` over the 4 JSON-RPC methods actually needed
  (`initialize`, `tools/list`, `tools/call`, `notifications/initialized`).
  This works because each `mcp_message` control request already carries
  one complete JSON-RPC call; there's no need to simulate a persistent
  bidirectional MCP session (which is why the Python reference's approach
  — reusing the real `mcp` Python package's `Server.run()` and in-memory
  transport machinery — doesn't have a lightweight Go equivalent worth
  chasing). Confirmed with the user 2026-08-27: proceed on this basis.

## Acceptance Criteria

### A. Tool & server registration API

1. Public types (all in a new `mcp.go`, naming matches this repo's
   existing `Mcp`-prefixed type family from phase 1's `McpStatusResponse`/
   `McpServerStatus`/`McpToolAnnotations` — see Decisions for the exact
   casing rationale):
   ```go
   type McpContent struct {
       Type     string // "text" | "image"
       Text     string
       Data     string // base64, image only
       MimeType string // image only
   }
   type McpToolResult struct {
       Content []McpContent
       IsError bool
   }
   type McpToolHandler func(ctx context.Context, args map[string]any) (*McpToolResult, error)
   type McpToolAnnotations struct {
       Title           string
       ReadOnlyHint    *bool
       DestructiveHint *bool
       IdempotentHint  *bool
       OpenWorldHint   *bool
   }
   ```
   (`McpToolAnnotations` already exists from phase 1 as the MCP-status
   *response* shape with different field names/casing — `ReadOnly`/
   `Destructive`/`OpenWorld` bool, no pointers, no `Title`/`IdempotentHint`.
   These are genuinely different wire shapes for different purposes per
   the original Python research — do not merge or rename the existing
   type; this phase needs its own, name it `McpToolAnnotations` only if it
   doesn't collide, otherwise `SdkMcpToolAnnotations` — resolve the exact
   name at implementation time by checking for a compile collision, and
   prefer the more specific name if one is needed.)
2. `NewMcpTool(name, description string, inputSchema map[string]any, handler McpToolHandler, opts ...McpToolOption) *McpTool`
   plus `func WithMcpToolAnnotations(ann *McpToolAnnotations) McpToolOption`.
   `McpTool` itself is an opaque struct with no exported fields (matches
   the reference's `Name()`/`Description()`/`InputSchema()`/`Annotations()`
   accessor pattern).
3. `NewSdkMcpServer(name, version string, tools ...*McpTool) *SdkMcpServer`
   — a thread-safe named registry of tools (`ListTools`/`CallTool`
   internal methods, mutex-guarded, matching the reference's
   `sync.RWMutex` pattern since concurrent tool calls must be supported
   per phase 1's inbound-control-request-per-goroutine design).
4. `WithSDKMcpServer(server *SdkMcpServer) Option` registers a server on
   `Client` construction. Multiple calls register multiple distinctly-
   named servers (error at `New()` time — not silently overwrite — if two
   calls register servers with the same name).
5. At `New()` time, every registered SDK server contributes an entry to
   the `--mcp-config` payload: `{"type":"sdk","name":"<name>"}` (no
   `instance`/tool list — the CLI discovers tools later via the in-process
   `tools/list` call once the control channel is up, matching the Python
   reference's launch-time behavior). This is **additive** to the existing
   `WithMCPConfig(string)` option from phase 1 (external stdio/sse/http
   servers, raw JSON/path passthrough) — do not remove or change that
   option's signature. If both are used together, merge: parse
   `WithMCPConfig`'s value (if it's inline JSON, not a file path — see
   Decisions for how to tell the difference) for its `mcpServers` object,
   union it with the SDK-server entries this phase generates, and send the
   merged object as the final `--mcp-config` JSON. If `WithMCPConfig`'s
   value is a file path (not inline JSON) and SDK servers are also
   registered, this is a genuine limitation — document it rather than
   attempting to read/merge into a caller-supplied file; the two must not
   both be set with a file-path `WithMCPConfig` (return an error from
   `New()` in that specific combination rather than silently dropping
   either).

### B. Wire protocol — `mcp_message` control request handling

6. Replace phase 1's stub for `mcp_message` in `handleControlRequest`
   (`engine.go`) with real dispatch. Incoming shape (unchanged from what
   phase 1 documented and pinned as unsupported):
   ```json
   {"type":"control_request","request_id":"<id>","request":{
     "subtype":"mcp_message","server_name":"<name>","message":{...JSON-RPC 2.0 request...}
   }}
   ```
   - `server_name` or `message` missing/empty → today's existing generic
     control-response error path: `{"subtype":"error","error":"Missing server_name or message for MCP request"}`.
   - `server_name` not found in the registered servers → the control
     request still SUCCEEDS at the envelope level, but the inner JSON-RPC
     payload is an error: `{"subtype":"success","response":{"mcp_response":{"jsonrpc":"2.0","id":<msg.id>,"error":{"code":-32601,"message":"server '<name>' not found"}}}}`.
   - Otherwise, dispatch the JSON-RPC `message.method`:
     - `"initialize"` → `{"jsonrpc":"2.0","id":<id>,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":<server.Name()>,"version":<server.Version()>}}}`
     - `"tools/list"` → `{"jsonrpc":"2.0","id":<id>,"result":{"tools":[{"name","description","inputSchema","annotations"?}...]}}` (one entry per registered tool; `annotations` present only if the tool had `WithMcpToolAnnotations` set, omitting zero-value fields)
     - `"tools/call"` → look up `params.name`, run its handler with `params.arguments` (default `{}` if absent); success → `{"jsonrpc":"2.0","id":<id>,"result":{"content":[...],"isError":<bool, omitted if false>}}` where each content item is `{"type":"text","text":...}` or `{"type":"image","data":...,"mimeType":...}`; handler error / panic / unknown tool name / nil-handler → still a JSON-RPC **success** response whose `result.isError` is `true` with a text content block describing the failure (matches both references: tool-level failures are not JSON-RPC protocol errors)
     - `"notifications/initialized"` → `{"jsonrpc":"2.0","result":{}}` (no `id` — it's a notification, not a request, but the outer control_request still needs an ack)
     - unknown method → JSON-RPC error `{"code":-32601,"message":"method '<method>' not found"}` (a protocol-level error this time, not a tool-level one, since it's the dispatch layer itself that doesn't recognize the method)
   - Any panic inside a tool handler is recovered (not allowed to crash the
     goroutine/process) and converted to the same handler-error path as
     Point 6's `tools/call` error case.
   - The whole dispatch result (success or JSON-RPC error) is wrapped in
     the standard control_response envelope: `{"type":"control_response","response":{"subtype":"success","request_id":"<id>","response":{"mcp_response":{...}}}}`
     — note `mcp_response` is always present on success, even when it
     itself carries a JSON-RPC-level error object; only a genuinely
     malformed control request (missing `server_name`/`message`) uses the
     top-level `"subtype":"error"` envelope.
7. Concurrent tool calls (including to different servers, and multiple
   calls to the same server) must be safely concurrent — this falls out of
   phase 1's existing per-control-request-goroutine dispatch plus
   `SdkMcpServer`'s internal mutex; no new synchronization primitive should
   be needed at the `handleControlRequest` level itself.

## Test Scenarios

**Registration**
- `NewSdkMcpServer` + `WithSDKMcpServer` produces the correct
  `{"type":"sdk","name":...}` entry in the `--mcp-config` argv (exact-argv
  assertion, matching phase 1's flag-test style).
- Two `WithSDKMcpServer` calls with distinct names both appear; two calls
  with the same name produce a `New()` error.
- `WithSDKMcpServer` combined with `WithMCPConfig` (inline JSON, not a
  path) merges both into one `--mcp-config` payload; combined with a
  file-path `WithMCPConfig` returns a `New()` error per AC 5's documented
  limitation.
- No SDK servers registered → `mcp_message` still returns "unsupported
  control request subtype" (regression pin, matching phase 1's existing
  test).

**Wire protocol — via the fake-CLI harness, scripting `mcp_message`
control requests after `initialize` succeeds**
- Full round trip: fake CLI sends `tools/call` for a registered tool →
  handler runs → response's `mcp_response.result.content` matches what the
  handler returned.
- `tools/list` response enumerates every registered tool across every
  registered server correctly scoped to the requested `server_name`.
- Unregistered `server_name` → JSON-RPC `-32601` inside a successful
  control_response envelope (not a top-level control-response error).
- Missing `server_name`/`message` on the control request itself → the
  top-level `"subtype":"error"` envelope (regression-distinguishing this
  from the previous case).
- Unknown tool name, and a handler that returns an error → both produce
  `isError:true` JSON-RPC **success** responses, not JSON-RPC errors.
- A handler that panics is recovered and surfaces as an `isError:true`
  response, not a crashed test process.
- Unknown JSON-RPC method (something other than the 4 handled) → JSON-RPC
  `-32601` "method not found".
- Two concurrent `tools/call` requests (to the same server, and to two
  different servers) both complete correctly and don't deadlock or
  interfere — mirrors phase 1's `TestClient_HooksSameEventDispatchedConcurrently`
  pattern (prove both are genuinely in flight simultaneously before either
  releases).

## Decisions

- **Zero new dependencies, hand-rolled JSON-RPC dispatch** — confirmed
  with the user 2026-08-27 after reading a community Go SDK's independent
  implementation of this exact feature, which validates this as sufficient
  (168-star project, in real use) rather than a corner cut. This resolves
  what would otherwise have been an AGENTS.md rule (d) stop-and-ask
  architecture decision.
- **`WithSDKMcpServer` is additive, not a replacement for `WithMCPConfig`**
  — `WithMCPConfig(string)` (phase 1, already shipped) stays exactly as
  it is; this phase does not touch its signature or behavior for callers
  who don't use SDK servers. The inline-JSON-vs-file-path merge limitation
  (AC 5) is a known, documented rough edge, not a design flaw to solve
  more elaborately in this phase — revisit only if it turns out to matter
  in practice.
- **Naming**: new types get the `Mcp`-prefix casing already established by
  phase 1's `McpStatusResponse`/`McpServerStatus`/`McpToolAnnotations`
  (mixed-case `Mcp`, not all-caps `MCP`) even though that's a minor
  deviation from strict Go acronym style — consistency with already-shipped
  public API matters more than fixing a cosmetic style question after the
  fact. The new option function is `WithSDKMcpServer` (all-caps `SDK`,
  matching the existing all-caps convention for acronyms leading an
  identifier like `WithMCPConfig`/`GetMCPStatus`).
- **No `WithMcpToolAnnotations` on the phase-1 `McpToolAnnotations`
  reuse**: phase 1's `McpToolAnnotations` (from `GetMCPStatus`'s response
  shape: `ReadOnly`/`Destructive`/`OpenWorld` bool, no pointers) and this
  phase's tool-registration annotations (`ReadOnlyHint`/`DestructiveHint`/
  `IdempotentHint`/`OpenWorldHint` `*bool`, plus `Title`) are genuinely
  different wire shapes serving different purposes (one is what the CLI
  reports back about a *discovered* tool's behavior; the other is what a
  caller declares about a tool *they're defining*) — implementer should
  give this phase's type a distinct name if `McpToolAnnotations` collides,
  rather than force one shape to serve both purposes.

## Progress

Not started.

## Validation

Not yet applicable.
