# claude-agent-sdk-go

An unofficial Go client for [Claude Code](https://claude.com/product/claude-code)'s headless/programmatic mode (`--output-format stream-json --input-format stream-json`).

This is **not** the [Agent Client Protocol](https://agentclientprotocol.com) — Claude Code has its own newline-delimited JSON wire protocol over stdio, distinct from ACP's JSON-RPC envelope. If you need an ACP client instead (for GLM or other ACP-speaking agents), this package isn't it.

There's no official Go SDK from Anthropic yet (only [TypeScript](https://github.com/anthropics/claude-agent-sdk-typescript) and [Python](https://github.com/anthropics/claude-agent-sdk-python)). This package's wire-protocol behavior — the CLI flags, the JSON message shapes, the `can_use_tool` control-request/response permission handshake — is verified against the real, open-source Python SDK's implementation, not guessed from CLI docs.

Extracted from [spacingmind/smind](https://github.com/spacingmind/smind), a self-hosted coding-agent platform, where it's used to drive Claude Code as one of several pluggable agent backends.

## Install

```sh
go get github.com/spacingmind/claude-agent-sdk-go
```

Requires the `claude` CLI to be installed and authenticated on `$PATH` (or pass `WithCLIPath` to point at a specific install).

## Usage

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

`client.Prompt` streams every message the CLI emits onto `updates` incrementally as it arrives (not buffered until the turn ends), and returns once the turn's terminal `result` message is received. `updates` is always closed before `Prompt` returns.

Unlike an [ACP](https://agentclientprotocol.com) agent, Claude Code reads and writes the filesystem directly through its own working directory (`cmd.Dir`, set to `worktreePath`) — it does not delegate file access back to the client over the wire, so this package has no filesystem-callback plumbing.

## Permission handling

Claude Code can send a `can_use_tool` control request mid-turn asking whether a tool call should proceed. `PermissionPolicy` is the seam for deciding:

```go
type PermissionPolicy interface {
    Decide(ctx context.Context, req CanUseToolRequest) (allow bool, updatedInput map[string]any, denyMessage string, err error)
}
```

Two built-in policies: `AutoApprovePolicy` and `AutoDenyPolicy`. The default (`AutoDenyPolicy`, paired with `--permission-mode default`) favors safety over convenience when nothing else is deciding — see the doc comment on `New` for the full reasoning. Implement `PermissionPolicy` yourself to wire up a human-in-the-loop decision (e.g. a UI prompt).

## Status

Used in production by [smind](https://github.com/spacingmind/smind). The public API may still change — this hasn't been tagged `v1` yet.

## License

MIT
