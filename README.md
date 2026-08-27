# claude-agent-sdk-go

[![CI](https://github.com/spacingmind/claude-agent-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/spacingmind/claude-agent-sdk-go/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/spacingmind/claude-agent-sdk-go)](https://goreportcard.com/report/github.com/spacingmind/claude-agent-sdk-go)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/spacingmind/claude-agent-sdk-go)](https://pkg.go.dev/github.com/spacingmind/claude-agent-sdk-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

An unofficial Go client for [Claude Code](https://claude.com/product/claude-code)'s headless/programmatic mode (`--output-format stream-json --input-format stream-json`).

This is **not** the [Agent Client Protocol](https://agentclientprotocol.com) — Claude Code has its own newline-delimited JSON wire protocol over stdio, distinct from ACP's JSON-RPC envelope. If you need an ACP client instead (for GLM or other ACP-speaking agents), this package isn't it.

There's no official Go SDK from Anthropic yet (only [TypeScript](https://github.com/anthropics/claude-agent-sdk-typescript) and [Python](https://github.com/anthropics/claude-agent-sdk-python)). This package's wire-protocol behavior — the CLI flags, the JSON message shapes, the `can_use_tool` control-request/response permission handshake — is verified against the real, open-source Python SDK's implementation, not guessed from CLI docs.

Extracted from [spacingmind/smind](https://github.com/spacingmind/smind), a self-hosted coding-agent platform, where it's used to drive Claude Code as one of several pluggable agent backends.

## Install

```sh
go get github.com/spacingmind/claude-agent-sdk-go
```

Requires the `claude` CLI to be installed and authenticated on `$PATH` (or pass `WithCLIPath` to point at a specific install).

Unlike an [ACP](https://agentclientprotocol.com) agent, Claude Code reads and writes the filesystem directly through its own working directory (`cmd.Dir`, set to the path passed to `New`) — it does not delegate file access back to the client over the wire, so this package has no filesystem-callback plumbing.

## Quickstart

```go
client, err := claudecode.New(worktreePath, claudecode.WithPermissionMode("acceptEdits"))
if err != nil {
    log.Fatal(err)
}
defer client.Close()

updates := make(chan claudecode.Message)
go func() {
    for msg := range updates {
        if am, ok := msg.(claudecode.AssistantMessage); ok {
            for _, block := range am.Content {
                if tb, ok := block.(claudecode.TextBlock); ok {
                    fmt.Print(tb.Text)
                }
            }
        }
    }
}()

result, err := client.Prompt(ctx, "fix the failing test in foo_test.go", updates)
```

`Prompt` sends one user turn, streams every message the CLI emits onto `updates` incrementally as it arrives (not buffered until the turn ends), and returns the turn's terminal `ResultMessage` once it's received. `updates` is always closed before `Prompt` returns, including on error.

## Streaming / persistent client

`Prompt` is a convenience wrapper. The primary API shape is a persistent client: `Query` sends a user turn without waiting for the reply, `ReceiveMessages` exposes the message stream for the client's whole lifetime, and `ReceiveResponse` scopes that stream to a single turn (closing after the next `ResultMessage`).

```go
client, err := claudecode.New(worktreePath, claudecode.WithPermissionMode("acceptEdits"))
if err != nil {
    log.Fatal(err)
}
defer client.Close()

if err := client.Query(ctx, "summarize this repo"); err != nil {
    log.Fatal(err)
}

// Multi-turn: the stream stays open across turns; ReceiveResponse ends at
// each turn's ResultMessage.
for i, question := range []string{"summarize this repo", "now list its flaws"} {
    if i > 0 {
        if err := client.Query(ctx, question); err != nil {
            log.Fatal(err)
        }
    }
    for msg := range client.ReceiveResponse(ctx) {
        switch m := msg.(type) {
        case claudecode.AssistantMessage:
            for _, block := range m.Content {
                if tb, ok := block.(claudecode.TextBlock); ok {
                    fmt.Print(tb.Text)
                }
            }
        case claudecode.ResultMessage:
            fmt.Printf("\n-- turn done in %d turns, $%.4f --\n", m.NumTurns, m.TotalCostUSD)
        }
    }
}
```

`QueryWithSession` targets a specific session ID (empty = the CLI's default session). Use `ReceiveMessages` directly — instead of `ReceiveResponse` — when you want one long-lived consumer fan-out across turns.

The decoded message types are: `SystemMessage` (plus typed task-lifecycle variants `TaskStartedMessage`, `TaskProgressMessage`, `TaskNotificationMessage`, `TaskUpdatedMessage`, and hook-event `HookEventMessage`), `AssistantMessage`, `UserMessage` (echoed user turns including tool results), `ResultMessage`, `StreamEvent` (raw API deltas, only with `WithIncludePartialMessages`), `RateLimitEvent`, and `ConversationResetMessage`. Content blocks (`TextBlock`, `ThinkingBlock`, `ToolUseBlock`, `ToolResultBlock`, `ServerToolUseBlock`, `ServerToolResultBlock`) are interfaces; unrecognized block types pass through as `RawBlock` rather than breaking parsing.

## Control protocol

Beyond the message stream, the CLI accepts mid-session control requests. The client handles the inbound half automatically (`can_use_tool` via your `PermissionPolicy`, `hook_callback` via registered hooks, `mcp_message` via in-process SDK MCP servers) and exposes the outbound half as methods:

| Method | Description |
| --- | --- |
| `Interrupt` | Abort the current turn. |
| `SetPermissionMode` | Switch permission mode mid-session (e.g. `"acceptEdits"`, `"plan"`). |
| `SetModel` | Switch the model mid-session (`nil` resets to the default). |
| `RewindFiles` | Rewind file changes back to the state at a given user message. |
| `GetMCPStatus` | Report status of the CLI's configured MCP servers. |
| `GetContextUsage` | Report the context-window usage breakdown. |
| `ReconnectMCPServer` | Ask the CLI to reconnect a named MCP server. |
| `ToggleMCPServer` | Enable or disable a named MCP server. |
| `StopTask` | Stop a background task by ID. |

## Permission handling

Claude Code can send a `can_use_tool` control request mid-turn asking whether a tool call should proceed. `PermissionPolicy` is the seam for deciding:

```go
type PermissionPolicy interface {
    Decide(ctx context.Context, req CanUseToolRequest) (allow bool, updatedInput map[string]any, denyMessage string, updatedPermissions []map[string]any, interrupt bool, err error)
}
```

- `allow` — permit the tool use.
- `updatedInput` — when non-nil on an allow, replaces the tool's input before it runs (e.g. rewrite a command).
- `denyMessage` — surfaced to the model as the deny reason.
- `updatedPermissions` — when non-nil on an allow, passed back to the CLI as session permission updates.
- `interrupt` — when true on a deny, asks the CLI to abort the whole turn.

Two built-in policies: `AutoApprovePolicy` and `AutoDenyPolicy`. The default (`AutoDenyPolicy`, paired with `--permission-mode default`) favors safety over convenience when nothing else is deciding — see the doc comment on `New` for the full reasoning. Even with a permissive mode set via `WithPermissionMode`, the CLI can still send `can_use_tool` for decisions the mode doesn't cover. Implement `PermissionPolicy` yourself to wire up a human-in-the-loop decision (e.g. a UI prompt).

## Hooks

Register hook callbacks with `WithHooks` at `New()` time. Callbacks are minted IDs during the initialize handshake and dispatched when the CLI sends `hook_callback` control requests — nothing runs out of process.

```go
c, err := claudecode.New(worktreePath,
    claudecode.WithHooks(map[claudecode.HookEvent][]claudecode.HookMatcher{
        claudecode.HookEventPreToolUse: {{
            Matcher: "Bash", // tool-name pattern; empty matches all
            Hooks: []claudecode.HookCallback{
                func(ctx context.Context, input map[string]any, toolUseID string) (*claudecode.HookJSONOutput, error) {
                    in, err := claudecode.DecodeHookInput[claudecode.PreToolUseHookInput](input)
                    if err != nil {
                        return nil, err
                    }
                    deny := in.ToolName == "Bash"
                    return &claudecode.HookJSONOutput{Decision: "approve"}, nil
                },
            },
            Timeout: 30 * time.Second,
        }},
    }),
)
```

Supported events: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit`, `Stop`, `SubagentStop`, `PreCompact`, `Notification`, `SubagentStart`, `PermissionRequest`. Each has a typed input struct (`PreToolUseHookInput`, `PostToolUseHookInput`, ...) you can decode with the generic `DecodeHookInput[T]`; a `HookJSONOutput` return value controls behavior (`Decision`, `Continue`, `SystemMessage`, ...). Enable `WithIncludeHookEvents` to additionally surface hook lifecycle events on the message stream.

## Session resume & listing

Resume a previous conversation by supplying session options at `New()` time: `WithResume(sessionID)`, `WithContinueConversation` (most recent session), `WithSessionID` (pin the ID of a new session), `WithForkSession`, `WithResumeSessionAt`, `WithResumeDropsTurn`.

Separately, the package exposes read-only access to the CLI's local session store (the NDJSON transcripts the `claude` CLI itself writes under `$CLAUDE_CONFIG_DIR` or `~/.claude`):

- `ListSessions(ListSessionsOptions{...})` — list stored sessions, newest first, per project directory or across all projects; supports limit/offset and git-worktree inclusion.
- `GetSessionInfo(sessionID, directory)` — one session's metadata (summary, timestamps, cwd, branch).
- `GetSessionMessages(sessionID, directory, limit, offset)` — the session's visible conversation, reconstructed from the transcript's `parentUuid` chain.
- `ListSubagents(sessionID, directory)` and `GetSubagentMessages(sessionID, agentID, directory, limit, offset)` — the same for stored subagent transcripts.

These are free functions, not `Client` methods — they read local files directly and have no relationship to a running CLI subprocess. Note: this package reads/writes only local `~/.claude` session files; there is no pluggable external session store.

## In-process MCP tools

Register Go functions as MCP tools the CLI's model can call, tunneled as `mcp_message` control requests over the same stdio connection — no subprocess server needed:

```go
addTool := claudecode.NewMcpTool("add", "add two numbers", map[string]any{
    "type": "object",
    "properties": map[string]any{
        "a": map[string]any{"type": "number"},
        "b": map[string]any{"type": "number"},
    },
    "required": []string{"a", "b"},
}, func(_ context.Context, args map[string]any) (*claudecode.McpToolResult, error) {
    a, _ := args["a"].(float64)
    b, _ := args["b"].(float64)
    return &claudecode.McpToolResult{
        Content: []claudecode.McpContent{{Type: "text", Text: fmt.Sprintf("%g", a+b)}},
    }, nil
})

server := claudecode.NewSdkMcpServer("my-tools", "1.0.0", addTool)

client, err := claudecode.New(worktreePath,
    claudecode.WithPermissionMode("acceptEdits"),
    claudecode.WithSDKMcpServer(server),
)
```

Handlers run in-process and concurrently with the message stream (panicking handlers are recovered and surfaced as tool errors, not crashes). `WithMcpToolAnnotations` attaches declared-behavior hints (read-only, destructive, ...) to a tool. For *external* MCP servers (stdio/sse/http), pass an `{"mcpServers": {...}}` config string via `WithMCPConfig` instead; inline-JSON configs merge with in-process SDK servers (SDK entries win on name collisions).

## Options overview

`New` takes functional options covering the Python/TypeScript SDKs' configuration surface. Grouped roughly:

- **System prompt**: `WithSystemPrompt`, `WithSystemPromptFile`, `WithAppendSystemPrompt`
- **Tools**: `WithTools`, `WithDefaultToolsPreset`, `WithAllowedTools`, `WithDisallowedTools`
- **Limits**: `WithMaxTurns`, `WithMaxBudgetUSD`, `WithTaskBudget`
- **Model & thinking**: `WithModel`, `WithFallbackModel`, `WithEffort`, `WithAdaptiveThinking`, `WithThinkingBudget`, `WithDisabledThinking`, `WithMaxThinkingTokens`
- **MCP**: `WithMCPConfig`, `WithSDKMcpServer`, `WithStrictMCPConfig`
- **Permissions**: `WithPermissionMode`, `WithPermissionPolicy`, `WithPermissionPromptTool`
- **Sessions**: `WithResume`, `WithContinueConversation`, `WithSessionID`, `WithForkSession`, `WithResumeSessionAt`, `WithResumeDropsTurn`
- **Streaming extras**: `WithIncludePartialMessages`, `WithIncludeHookEvents`
- **Environment & misc**: `WithEnv`, `WithCLIPath`, `WithLogWriter`, `WithSettings`, `WithSettingSources`, `WithAddDirs`, `WithPluginDirs`, `WithBetas`, `WithJSONSchema`, `WithExtraArgs` (raw flag passthrough)
- **Hooks**: `WithHooks`

See [godoc](https://pkg.go.dev/github.com/spacingmind/claude-agent-sdk-go) for the full list and each option's exact wire behavior.

## Status

Used in production by [smind](https://github.com/spacingmind/smind). This started as a small extraction (~700 lines, one-shot prompts only) and has grown well past that original scope: it now covers most of the Python/TypeScript reference SDKs' surface per an internal parity audit — the persistent streaming client, the full control-protocol handshake, hooks, local session resume/listing, and the in-process SDK-MCP tool bridge documented above. Still pre-`v1`: the public API may still shift.

Known gaps: local-disk session resume only (no pluggable external session store), and no interactive/human-in-the-loop permission UI — the `PermissionPolicy` interface is the seam one plugs into.

## License

MIT
