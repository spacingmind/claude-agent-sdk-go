# Streaming Client & Control Protocol (SDK parity, phase 1)

Phase 1 of bringing `claude-agent-sdk-go` to 1:1 feature parity with the
official Python SDK (`refs/claude-agent-sdk-python`), per the research pass
recorded below. Scope was narrowed with the user on 2026-08-27 (see
Decisions) to the foundational engine: a persistent streaming client, the
full control-protocol handshake, CLI-flag/options parity, message/content-
block parity, and hooks. The in-process SDK-MCP tool bridge and session
persistence (local resume, and — if ever needed — a pluggable
`SessionStore`) are explicitly deferred to their own follow-on plans.

## Acceptance Criteria

### A. Persistent client / concurrency model

1. A `Client` can be constructed and used for more than one turn without
   re-spawning the CLI subprocess, and control-protocol traffic (interrupt,
   set_permission_mode, hook callbacks, etc.) works independent of whether a
   `Prompt`/`Send` call currently has an in-flight turn.
2. Exactly one goroutine reads and decodes the CLI's stdout NDJSON stream for
   the lifetime of a `Client`; it is started once (at `New`, matching this
   repo's existing eager-connect behavior — see Decisions) and is the sole
   writer to the outbound message channel and the sole reader of the
   transport.
3. All writes to the CLI's stdin (prompts, control requests, control
   responses) are serialized through one synchronization point, safe for
   concurrent callers (e.g. a caller sending a new prompt while another
   goroutine calls `Interrupt`).
4. Regular content messages (system/assistant/user/result/stream_event/
   rate_limit_event/conversation_reset) are delivered to callers via a
   persistent message stream that is not scoped to a single `Prompt`/`Send`
   call — a caller can observe messages the CLI pushes unprompted.
5. `Prompt` (the existing one-shot convenience method) continues to work
   unchanged from the caller's perspective: send one turn, stream messages,
   return on the terminal `ResultMessage`. It is reimplemented on top of the
   new persistent stream/send primitives rather than owning its own read
   loop.
6. Outbound control requests (`interrupt`, `set_permission_mode`, `set_model`,
   `get_mcp_status`, `get_context_usage`, `reconnect_mcp_server`,
   `toggle_mcp_server`, `stop_task`, `initialize`) are correlated to their
   response by a generated `request_id`, block the calling goroutine until a
   matching `control_response` arrives or a timeout elapses (default 60s,
   `initialize` uses a longer configurable timeout), and surface the CLI's
   `"subtype": "error"` response as a Go error.
7. Inbound control requests (`can_use_tool`, `hook_callback`) are each
   handled in their own goroutine, individually cancellable when the CLI
   sends a matching `control_cancel_request` — the read loop never blocks
   waiting for a handler to finish, and multiple `can_use_tool`/hook calls
   can be outstanding concurrently.
8. On `Close`, all pending outbound control requests are force-resolved with
   a terminal error (not left to time out), all in-flight inbound
   control-request handlers are cancelled, and the CLI subprocess is torn
   down via the existing stdin-close → grace period → force-kill sequence.
9. If the read loop exits (CLI crash, stdout EOF, decode error), every
   pending outbound control request and the message stream itself
   terminates with an error a caller can distinguish from "clean end of
   turn."

### B. Outbound control protocol (SDK → CLI)

10. `Client` exposes methods sending each of the following control requests,
    matching these exact wire shapes (envelope always
    `{"type":"control_request","request_id":"<generated>","request":{"subtype":"<name>",...}}`):
    - `interrupt()` → `{"subtype":"interrupt"}`
    - `SetPermissionMode(mode)` → `{"subtype":"set_permission_mode","mode":<mode>}`
    - `SetModel(model)` → `{"subtype":"set_model","model":<model or null>}`
    - `GetMCPStatus()` → `{"subtype":"mcp_status"}`, response deserializes
      into a Go `McpStatusResponse` (`{"mcpServers":[...]}`, per-server
      `name`/`status`/`serverInfo?`/`error?`/`config`/`scope`/`tools?`)
    - `GetContextUsage()` → `{"subtype":"get_context_usage"}`
    - `ReconnectMCPServer(serverName)` → `{"subtype":"mcp_reconnect","serverName":<name>}`
      (note: camelCase `serverName`, not snake_case — verified against
      Python source comments, not a porting artifact to "fix")
    - `ToggleMCPServer(serverName, enabled)` → `{"subtype":"mcp_toggle","serverName":<name>,"enabled":<bool>}`
    - `StopTask(taskID)` → `{"subtype":"stop_task","task_id":<id>}`
    - `RewindFiles(userMessageID)` → `{"subtype":"rewind_files","user_message_id":<id>}`
      (added after cross-checking a community Go port,
      github.com/severity1/claude-agent-sdk-go, which independently
      implements this same control surface — the omission here was a gap
      in the original research pass, not a deliberate exclusion)
11. `initialize` is sent once per connection before any prompt is accepted,
    carrying `hooks` (hook-matcher config with SDK-minted callback IDs, not
    the callbacks themselves — see F), and is a no-op if no hooks/agents/
    skills options are configured (still sent, since the CLI's control
    channel setup depends on it having run).
12. `request_id` generation is collision-safe under concurrent callers (e.g.
    monotonic counter + random suffix, mirroring Python's
    `req_<counter>_<8 hex chars>` — exact string format is not wire-load-
    bearing, only uniqueness and opaqueness to the CLI are).

### C. Inbound control protocol (CLI → SDK)

13. `can_use_tool` handling is extended beyond today's `allow`/`deny`/
    `updatedInput` to also: accept and pass through `updatedPermissions` on
    allow, accept and pass through `interrupt` on deny, and pass the
    request's `title`/`display_name`/`description`/`decision_reason`/
    `blocked_path`/`agent_id` fields into the `PermissionPolicy.Decide` call
    (extending `CanUseToolRequest`, additively — existing fields unchanged).
14. `hook_callback` requests (`{"subtype":"hook_callback","callback_id":<id>,"input":<any>,"tool_use_id":<id-or-null>}`)
    are dispatched to the Go callback registered under that ID at
    `initialize` time; response is the callback's `HookJSONOutput` returned
    verbatim as `response_data` (no wrapping key), with Python's `async_`/
    `continue_` renaming irrelevant in Go (struct fields use `Async`/
    `Continue` with `json:"async"`/`json:"continue"` tags directly).
15. Any control-request subtype not in {`can_use_tool`, `hook_callback`,
    `control_cancel_request`} still returns today's clean
    `{"subtype":"error","error":"unsupported control request subtype ..."}`
    response — including `mcp_message` (SDK-MCP bridge, explicitly deferred).
16. `control_cancel_request` (`{"type":"control_cancel_request","request_id":"<id>"}`)
    cancels the in-flight inbound-handler goroutine for that ID if still
    running; no response is sent for a cancelled handler.
17. A malformed or unrecognized control message (bad JSON, missing
    `request_id`, unknown envelope `type`) is skipped without aborting the
    read loop or the connection — matches this repo's existing "skip
    malformed lines" policy in `decodeLine`.

### D. CLI flag / options parity

18. `Client` construction accepts functional options covering every
    `ClaudeAgentOptions` field that maps to a CLI flag, **excluding**
    session-related flags (`--continue`, `--resume`, `--session-id`,
    `--fork-session`, `--resume-session-at`, `--resume-drops-turn`,
    `--session-mirror`) and the SDK-MCP `type:"sdk"` server-config path —
    both deferred with their respective follow-on plans. In scope: system
    prompt (string/file/preset+append), tools/allowed-tools/disallowed-tools,
    max-turns, max-budget-usd, task-budget, model, fallback-model, betas,
    permission-prompt-tool-name, permission-mode (unchanged default
    behavior — see Decisions), settings/sandbox, add-dir (repeatable),
    mcp-config (external servers only: stdio/sse/http, not `type:"sdk"`),
    include-partial-messages, include-hook-events, strict-mcp-config,
    setting-sources/skills, plugin-dir (repeatable, local plugins only),
    extra-args passthrough, thinking/max-thinking-tokens/thinking-display,
    effort, json-schema (structured `output_format`).
19. Flags requiring `=`-joined `--flag=value` form to guard against a
    dash-leading value being misparsed as a separate flag (Python does this
    for a specific subset) are only relevant for flags carrying
    caller-controlled string values — apply the same guard wherever this
    port adds a flag whose value isn't a fixed enum/bool.
20. Environment variables set on the subprocess: strip inherited
    `CLAUDECODE`, set `CLAUDE_CODE_ENTRYPOINT` (Go-appropriate value, e.g.
    `sdk-go`) unless overridden by a caller-supplied env option, layer any
    caller-supplied env vars on top of the inherited environment (not a full
    replacement — this is a behavior change from today's `withExtraEnv`,
    which currently replaces `cmd.Env` wholesale; see Decisions).
21. `extra_args`-equivalent escape hatch exists (a functional option
    accepting raw `--flag[=value]` pairs) so options this port hasn't
    modeled yet can still reach the CLI.

### E. Message & content-block parity

22. `ContentBlock` gains: `ThinkingBlock{Thinking, Signature}`,
    `ToolResultBlock{ToolUseID, Content, IsError}` (parseable from both
    `assistant` and `user` messages), `ServerToolUseBlock{ID, Name, Input}`,
    `ServerToolResultBlock{ToolUseID, Content}`. Unrecognized block types
    remain silently dropped inside `assistant`/`user` content arrays
    (matches Python's asymmetric behavior: unknown top-level message types
    are skip-with-log, unknown content-block types are skip-silent).
23. New top-level message types: `StreamEvent{UUID, SessionID, Event,
    ParentToolUseID}` (only emitted when partial-messages is enabled),
    `RateLimitEvent{RateLimitInfo, UUID, SessionID}`,
    `ConversationResetMessage{NewConversationID, UUID, SessionID}`.
24. The `SystemMessage` family gains typed variants for `task_started`,
    `task_progress`, `task_notification`, `task_updated` (defensively
    parsed — never errors on missing `patch`/`task_id`/`status`), and
    (when `include_hook_events` is set) `hook_started`/`hook_response` as
    `HookEventMessage`. Represented as a `SystemMessage` interface plus
    concrete structs (no Go inheritance available) discriminated by
    `Subtype()`, per the research recommendation — any subtype not
    explicitly modeled still decodes into today's generic `SystemMessage`
    catch-all.
25. `ResultMessage` gains the fields identified as missing:
    `DurationAPIMs`, `ModelUsage` (keyed by model, from wire key
    `modelUsage`), `DeferredToolUse`, `Errors`, `APIErrorStatus`,
    `TerminalReason`, `StructuredOutput`, `UUID`, `Origin`.
26. A malformed *known* message type (missing required field) is treated as
    a decode error on that line and skipped, consistent with this repo's
    existing `decodeLine` policy — deliberately looser than Python's raise-
    on-malformed-known-type behavior, since this repo has already decided
    "don't fail the whole turn over one bad line" (see Decisions).

### F. Hooks

27. A functional option registers hook matchers per `HookEvent`
    (`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit`,
    `Stop`, `SubagentStop`, `PreCompact`, `Notification`, `SubagentStart`,
    `PermissionRequest`), each matcher carrying a tool-name pattern, one or
    more Go callback functions, and an optional timeout.
28. At `initialize` time, every registered callback is assigned a
    `hook_<n>` ID, the ID (not the callback) is sent to the CLI keyed by
    event/matcher, and the Go-side callback is looked up by ID when a
    `hook_callback` control request arrives (per C.14).
29. Matchers registered on the same event fire concurrently when the CLI
    invokes more than one for the same event (documented Python behavior,
    not enforced by any type — the Go dispatch must not serialize same-event
    matchers).
30. Each hook input variant (`PreToolUseHookInput`, `PostToolUseHookInput`,
    etc. — the 10 variants inventoried in Decisions) is a distinct Go type
    the callback receives, decoded from the control request's `input` field
    based on `hook_event_name`.

## Test Scenarios

**Client lifecycle / concurrency**
- Two sequential `Prompt` calls on one `Client` reuse the same subprocess
  (no second spawn).
- `Interrupt` succeeds (returns once acked) while a `Prompt` call has an
  in-flight turn streaming assistant content.
- Concurrent `SetPermissionMode` and `SetModel` calls from two goroutines
  both complete correctly, correlated to their own responses (fake CLI
  responds to both, out of order, with the wrong-order acks still landing
  on the right caller).
- `Close` during an in-flight `Prompt` unblocks that call with an error
  rather than hanging.
- Fake CLI sends a `control_response` for an unknown/already-timed-out
  `request_id` — no panic, silently ignored.
- Fake CLI closes stdout mid-turn (simulated crash) — any pending outbound
  control request unblocks with an error instead of hanging until timeout.

**Outbound control protocol**
- Each of the 8 outbound methods (B.10) sends the exact documented JSON
  shape (asserted against the fake CLI's captured stdin) and correctly
  unmarshals a canned success response.
- A `"subtype":"error"` response to any outbound method surfaces as a Go
  `error` carrying the CLI's error string.
- An outbound control request with no response within the timeout returns a
  timeout error and the pending-request map no longer holds it afterward
  (verified indirectly: a late response arriving after timeout is a no-op,
  doesn't panic).

**Inbound control protocol**
- `can_use_tool` round trip with `updatedPermissions` set on allow — verify
  it's included in the response only when the policy set it, omitted
  otherwise.
- `can_use_tool` deny with `interrupt: true` — verify the field is present
  only when set.
- `hook_callback` for a registered callback ID dispatches to the right Go
  function and returns its `HookJSONOutput` unwrapped (no extra nesting).
- `hook_callback` for an unregistered ID returns the documented error
  response.
- `mcp_message` control request still returns "unsupported subtype" (bridge
  deferred) — regression test pinning today's behavior so a future SDK-MCP
  plan changes it deliberately, not accidentally.
- `control_cancel_request` for an in-flight `can_use_tool` handler cancels
  it and no response is sent afterward (fake CLI asserts no line arrives
  for that request_id).
- A malformed control-request line (bad JSON, missing `request_id`) doesn't
  crash the read loop; subsequent well-formed traffic still works.

**CLI flags / options**
- For each newly supported option, constructing a `Client` with it set
  produces the exact documented flag in the spawned command (asserted via
  the fake-CLI harness capturing argv).
- Options left unset produce no flag (not an empty/default flag) — pinning
  the "omit rather than default" behavior for fields where Python omits.
- `extra_args`-equivalent option round-trips a raw flag through to argv
  unmodified.
- Env var test: spawned subprocess env has `CLAUDECODE` absent even when
  present in the test-runner's own environment, and a caller-supplied env
  var is present alongside inherited vars (not replacing them).

**Messages / content blocks**
- Fake CLI emits a `thinking` content block on an `assistant` message —
  decodes to `ThinkingBlock` with `Signature` populated.
- Fake CLI emits a `tool_result` block on an *assistant* message (not just
  user) — decodes correctly.
- Fake CLI emits `stream_event`, `rate_limit_event`, `conversation_reset`
  top-level messages — each decodes to its typed struct; an old client that
  doesn't know about them (pre-this-change decodeLine) would have silently
  skipped them, so this is new coverage, not a behavior change to pin.
- Fake CLI emits `task_updated` with a malformed/partial `patch` — decode
  doesn't error, defaults applied per C.24's spec.
- `ResultMessage` with `modelUsage` populated decodes into `ModelUsage`
  keyed by model string.

**Hooks**
- A `PreToolUse` hook matcher registered via the functional option receives
  a `hook_callback` control request shaped as `PreToolUseHookInput` and its
  returned `HookJSONOutput` (including `hookSpecificOutput.permissionDecision`)
  round-trips correctly.
- Two matchers on the same event both fire when the fake CLI sends two
  `hook_callback` requests back-to-back without waiting for the first
  response — both must be able to be in flight concurrently (test would
  deadlock/timeout if the Go implementation serializes them).

## Decisions

- **Scope split (2026-08-27, user-confirmed):** this plan covers the
  streaming client, full control protocol (minus `mcp_message`), CLI-flag/
  options parity (minus session flags), message/content-block parity, and
  hooks. Two follow-on plans, not yet written: (1) SDK-MCP bridge
  (in-process tool registration — requires a new Go dependency or hand-
  rolled JSON-RPC dispatch, deferred because it's a real architecture
  decision, not just wire-shape work); (2) session lifecycle, scoped to
  **local-disk resume only** (`--resume`/`--continue`/`--fork-session`/
  `--session-id`/`--resume-session-at`/`--resume-drops-turn` flags, plus Go
  equivalents of Python's read-only `sessions.py` local listing/reading
  functions) — the pluggable `SessionStore` abstraction, resume
  materialization into a temp `CLAUDE_CONFIG_DIR`, `session_mutations`,
  `session_summary` folding, and the transcript-mirror batcher (~4000 lines
  of Python, built specifically to support external non-file-backed
  session storage) are explicitly out of scope unless a concrete need for
  externally-backed sessions shows up later.
- **Eager connect, not lazy (implementer decision, Go-idiom/backward-compat):**
  Python's `ClaudeSDKClient.connect()` lazily spawns the subprocess;
  this repo's `New()` already spawns eagerly and is used in production by
  smind. Keep eager-connect — changing this would be a breaking behavior
  change for an existing caller with no parity benefit that matters here
  (the CLI subprocess is cheap to spawn and this SDK is not used in a
  connect-then-idle-then-maybe-send pattern).
- **`Prompt` stays as a wrapper, not the primary API (implementer decision):**
  the persistent message stream + `Send`-style method become the primary
  primitives (matching Python's `query()`/`receive_messages()` split);
  `Prompt` is reimplemented on top of them for existing callers, not
  removed. Exact new method names are an implementation detail for the
  coding phase, not fixed by this plan.
- **Error hierarchy not yet specced:** Python's `_errors.py`
  (`ClaudeSDKError`, `CLIConnectionError`, `CLINotFoundError`,
  `ProcessError`, `ResultError`, `CLIJSONDecodeError`) was out of the
  original research reading list and is referenced but not detailed here.
  Needs its own short research pass before/during implementation — flagged
  rather than guessed at.
- **`env` merge semantics change:** today's `withExtraEnv` fully replaces
  `cmd.Env` (test-only, unexported). The public option this plan adds must
  merge onto the inherited environment instead (matching D.20) — this is a
  new public option, not a change to the existing test-only knob, so no
  backward-compat concern.
- **Malformed-known-message policy diverges from Python on purpose:** this
  repo already chose "skip a bad line rather than fail the whole turn" in
  `decodeLine`; Python raises `MessageParseError` on a malformed *known*
  message type. Keeping this repo's existing looser policy (E.26) rather
  than tightening it to match Python, since it's an already-made call in
  this codebase, not new territory this port needs to relitigate.
- **SIGTERM-before-SIGKILL escalation not included in this plan's
  acceptance criteria:** Python's `close()` does stdin-EOF-wait(5s) →
  SIGTERM-wait(5s) → SIGKILL-wait(5s); this repo does stdin-EOF-wait(5s) →
  SIGKILL directly. Worth doing eventually (Python's rationale: avoids
  interrupting an in-flight session-file write) but it's an orthogonal
  transport-robustness improvement, not something the control-protocol/
  streaming-client work depends on — left as a follow-up, not blocking this
  plan.

## Progress

- **Done** — Section D (CLI flag / options parity, AC 18-21): implemented on
  `sdk-parity/cli-flags-options` (commit `654a0ae`), merged to `main`.
  Table-driven tests in `flags_test.go` cover exact-argv assertions per
  option, unset-produces-no-flag, extra-args passthrough, and env merge
  (`CLAUDECODE` stripped, `CLAUDE_CODE_ENTRYPOINT` default + override,
  inherited vars preserved).
- **Done** — Section E (Message & content-block parity, AC 22-26):
  implemented on `sdk-parity/message-content-blocks` (commit `788a98a`),
  merged to `main`. Table-driven tests in `messages_test.go` cover all new
  content blocks, top-level message types, system subtypes (including
  `task_updated`'s defensive parsing), and `ResultMessage`'s new fields.
- **Done** — Sections A/B/C (persistent client / concurrency model,
  outbound + inbound control protocol, AC 1-17): implemented on
  `sdk-parity/streaming-client-engine`. `New()` keeps eager-connect and now
  starts one persistent read-loop goroutine and blocks on the `initialize`
  handshake before returning. `Prompt` is reimplemented over
  `Query`/`ReceiveResponse` (signature and observable behavior unchanged);
  new methods: `Query`, `QueryWithSession`, `ReceiveMessages`,
  `ReceiveResponse`, `Interrupt`, `SetPermissionMode`, `SetModel`,
  `RewindFiles`, `GetMCPStatus`, `GetContextUsage`, `ReconnectMCPServer`,
  `ToggleMCPServer`, `StopTask`. Outbound requests are correlated by a
  collision-safe `request_id` with a 60s default timeout and error/timeout
  force-removal from the pending map; CLI exit or `Close` force-resolves
  all pending requests. Inbound `can_use_tool` handlers run per-request
  (cancellable via `control_cancel_request`, no response written when
  cancelled) with the enriched request fields and the widened
  `PermissionPolicy.Decide` return tuple (`updatedPermissions` on allow,
  `interrupt` on deny, present-only-when-set on the wire); `hook_callback`
  dispatch scaffolding (unexported `HookCallback` map) is in place per the
  plan's phase-F boundary, and `mcp_message` still returns
  "unsupported control request subtype". Tests in `engine_test.go` cover
  the lifecycle/concurrency, outbound-shape, and inbound groups, including
  out-of-order response correlation, crash force-resolution, timeout
  cleanup, and the cancelled-handler no-response guarantee.
- **Done** — Section F (Hooks, AC 27-30): implemented on
  `sdk-parity/hooks`. `WithHooks(map[HookEvent][]HookMatcher)` registers
  per-event matchers; `New()` mints `hook_<n>` IDs for every callback,
  registers them into the callback map under `hookMu`, and sends them
  keyed by event/matcher in the `initialize` payload (`matcher`,
  `hookCallbackIds`, optional `timeout` in seconds; no `hooks` key at all
  when nothing is configured). `HookCallback`'s return type widened to
  `*HookJSONOutput` (nil = no output; existing dispatch's `out != nil`
  check works unchanged with the pointer type). The 10 typed hook-input
  structs plus the `DecodeHookInput[T]` generic decode helper live in
  `permission.go` for callbacks that want a typed view — dispatch still
  passes the raw map per the plan. Tests in `engine_test.go`
  (`hooks_roundtrip`, `hooks_concurrent` fake-CLI scenarios) cover
  registration wire shape, PreToolUse input decode, `HookJSONOutput`
  round-trip including `hookSpecificOutput.permissionDecision`, and
  concurrent same-event dispatch via a both-blocked-before-either-released
  channel proof.

## Validation

- Sections A/B/C verified via the `verify` skill on
  `sdk-parity/streaming-client-engine`: `go build -buildvcs=false ./...`,
  `go test -race ./...` (stable across `-count=2`, suite completes in ~7s,
  no leaked subprocesses), `go vet ./...`, `gofmt -l .` — all clean. Test
  scenarios for the lifecycle/concurrency, outbound, and inbound groups are
  covered in `engine_test.go` against scripted fake-CLI scenarios
  (`two_turns`, `interruptible`, `reorder`, `crash`, `capture_stdin`,
  `control_echo`, `control_traffic`, `policy_roundtrip`, `hook_dispatch`.
- Sections D/E verified on their branches (`654a0ae`, `788a98a`); see those
  commits' messages for the per-scenario mapping.
- Section F verified on `sdk-parity/hooks`: same four commands, all clean;
  `go test -race ./...` completes in ~7s with no hangs (the concurrent-
  dispatch test would deadlock under serialization rather than flake).

Final validation pass, Acceptance Criterion by criterion:

- **AC 1** — `TestClient_SequentialPromptsReuseSubprocess` (two turns, one
  process), `TestClient_ConcurrentControlRequestsCorrelateOutOfOrder`.
- **AC 2** — single `readLoop` goroutine started once in `New`
  (`engine.go`); all tests exercise it implicitly; out-of-order
  correlation test proves sole-reader dispatch.
- **AC 3** — serialized stdin writes via `transport.writeLine`
  (`transport.go`); `TestClient_ConcurrentControlRequestsCorrelateOutOfOrder`
  writes concurrently.
- **AC 4** — `ReceiveMessages` persistent stream; used by every Prompt-based
  test via `ReceiveResponse` forwarding.
- **AC 5** — `TestClient_SequentialPromptsReuseSubprocess` and
  `TestClient_InterruptMidTurn` exercise `Prompt` over the new engine.
- **AC 6** — `TestClient_OutboundControlWireShapes` (all subtypes),
  `TestClient_OutboundControlErrorResponse`, `TestClient_OutboundControlTimeout`.
- **AC 7** — `TestClient_HooksSameEventDispatchedConcurrently` (two
  same-event hooks in flight simultaneously); `hook_dispatch` scenario
  covers per-request goroutine + `control_cancel_request` cancellation.
- **AC 8** — `TestClient_CloseDuringPromptUnblocks`.
- **AC 9** — `TestClient_CrashForceResolvesPendingRequests`.
- **AC 10** — `TestClient_OutboundControlWireShapes` asserts each exact
  wire shape against captured stdin.
- **AC 11** — `TestClient_HooksRegisteredAndRoundTrip` asserts the
  `initialize` payload carries `hooks` with `hookCallbackIds`/`matcher`;
  other tests cover the plain no-hooks initialize (nil extra).
- **AC 12** — `TestClient_OutboundControlWireShapes` asserts unique,
  populated `request_id`s (`req_<counter>_<hex>` format in `nextRequestID`).
- **AC 13** — `TestClient_CanUseToolRoundTripFields` (enriched fields,
  `updatedPermissions` present-only-when-set, `interrupt` on deny).
- **AC 14** — `TestClient_HooksRegisteredAndRoundTrip` and
  `TestClient_HookCallbackDispatch` (`HookJSONOutput` verbatim as
  `response_data`, no wrapping key).
- **AC 15** — `TestClient_InboundControlEdgeCases` (`mcp_message` →
  "unsupported control request subtype" pin).
- **AC 16** — `hook_dispatch` scenario: cancelled handler never answers
  (next response is for `req-h3`, not `req-h2`).
- **AC 17** — `TestClient_UnknownControlResponseIgnored` and the malformed
  lines in `control_traffic` / `malformed` scenarios.
- **AC 18-21** — table-driven exact-argv tests in `flags_test.go` (D's
  commit `654a0ae`), including unset-produces-no-flag, extra-args
  passthrough, and env merge.
- **AC 22-26** — table-driven decode tests in `messages_test.go` (E's
  commit `788a98a`).
- **AC 27** — `WithHooks` option with all ten `HookEvent` constants and
  `HookMatcher{Matcher, Hooks, Timeout}`; exercised in
  `TestClient_HooksRegisteredAndRoundTrip`.
- **AC 28** — `hook_<n>` IDs minted in `New`, sent (not the callbacks) in
  the `initialize` payload, looked up on `hook_callback`;
  `TestClient_HooksRegisteredAndRoundTrip` end-to-end.
- **AC 29** — `TestClient_HooksSameEventDispatchedConcurrently`: both
  callbacks provably blocked simultaneously before either is released.
- **AC 30** — the 10 typed input structs + `DecodeHookInput[T]`;
  `TestClient_HooksRegisteredAndRoundTrip` decodes a real
  PreToolUse input through the helper.

All 30 acceptance criteria validated. `go build -buildvcs=false ./...`,
`go test -race -count=1 ./...`, `go vet ./...`, `gofmt -l .` — all clean.
