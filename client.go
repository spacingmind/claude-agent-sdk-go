// Package claudecode is a native Go client for Claude Code's headless
// programmatic mode: it spawns the `claude` CLI with
// --output-format/--input-format stream-json and speaks its newline-
// delimited JSON wire protocol directly, including the control-request
// permission handshake. It is not the Agent Client Protocol -- Claude Code
// has its own wire format, distinct from ACP's JSON-RPC envelope.
package claudecode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultCloseGracePeriod = 5 * time.Second

// Option configures a Client constructed by New.
type Option func(*options)

type systemPromptKind int

const (
	// systemPromptPlain covers both "unset" (Python sends --system-prompt
	// "" then) and a plain string prompt.
	systemPromptPlain systemPromptKind = iota
	systemPromptFile
	systemPromptPresetAppend
)

type systemPrompt struct {
	kind   systemPromptKind
	value  string
	append string
}

// toolsKind discriminates the tools option's wire forms: unset produces no
// flag, a preset produces `--tools default`, and a list produces
// `--tools <comma-joined>` (or `--tools ""` when empty).
type toolsKind int

const (
	toolsPreset toolsKind = iota
	toolsList
)

type toolsConfig struct {
	kind toolsKind
	list []string
}

type thinkingKind int

const (
	thinkingAdaptive thinkingKind = iota
	thinkingEnabled
	thinkingDisabled
)

type thinkingConfig struct {
	kind         thinkingKind
	budgetTokens int
	display      string
}

type options struct {
	permissionMode   string
	permissionPolicy PermissionPolicy
	logWriter        io.Writer
	cliPath          string
	extraEnv         []string
	closeGracePeriod time.Duration

	systemPrompt         *systemPrompt
	tools                *toolsConfig
	allowedTools         []string
	maxTurns             int
	maxBudgetUSD         *float64
	disallowedTools      []string
	taskBudget           *int
	model                string
	fallbackModel        string
	betas                []string
	permissionPromptTool string
	settings             string
	addDirs              []string
	mcpConfig            string
	sdkMcpServerList     []*SdkMcpServer
	includePartial       bool
	includeHookEvents    bool
	strictMcpConfig      bool
	settingSources       []string
	pluginDirs           []string
	extraArgs            map[string]*string
	thinking             *thinkingConfig
	maxThinkingTokens    *int
	effort               string
	jsonSchema           string
	env                  []string
	hooks                map[HookEvent][]HookMatcher

	continueConversation bool
	resume               string
	sessionID            string
	forkSession          bool
	resumeSessionAt      string
	resumeDropsTurn      *string
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

// WithSystemPrompt sets the system prompt to a plain string, sent as
// --system-prompt <prompt>. Passing "" matches the Python SDK's
// unset-prompt wire form (--system-prompt "").
func WithSystemPrompt(prompt string) Option {
	return func(o *options) {
		o.systemPrompt = &systemPrompt{kind: systemPromptPlain, value: prompt}
	}
}

// WithSystemPromptFile sets the system prompt from a file, sent as
// --system-prompt-file <path>.
func WithSystemPromptFile(path string) Option {
	return func(o *options) {
		o.systemPrompt = &systemPrompt{kind: systemPromptFile, value: path}
	}
}

// WithAppendSystemPrompt appends to the CLI's preset system prompt, sent as
// --append-system-prompt <text>.
func WithAppendSystemPrompt(text string) Option {
	return func(o *options) {
		o.systemPrompt = &systemPrompt{kind: systemPromptPresetAppend, append: text}
	}
}

// WithTools restricts the session to the named tools, sent as
// --tools <a,b,c>. An empty list sends --tools "" (no tools at all) --
// distinct from not calling WithTools, which omits the flag.
func WithTools(tools ...string) Option {
	return func(o *options) {
		o.tools = &toolsConfig{kind: toolsList, list: tools}
	}
}

// WithDefaultToolsPreset selects the CLI's default tools preset, sent as
// --tools default.
func WithDefaultToolsPreset() Option {
	return func(o *options) { o.tools = &toolsConfig{kind: toolsPreset} }
}

// WithAllowedTools pre-approves the named tools, sent as
// --allowedTools <a,b,c>. Only sent when non-empty.
func WithAllowedTools(tools ...string) Option {
	return func(o *options) { o.allowedTools = tools }
}

// WithMaxTurns caps the agentic loop at n turns, sent as --max-turns <n>.
// Only sent when n > 0.
func WithMaxTurns(n int) Option {
	return func(o *options) { o.maxTurns = n }
}

// WithMaxBudgetUSD caps per-run spend at n dollars, sent as
// --max-budget-usd <n>. Variadic so an explicit 0 is sent (matching the
// Python SDK's "is not None" condition) while leaving it unset omits the
// flag.
func WithMaxBudgetUSD(n ...float64) Option {
	return func(o *options) {
		if len(n) > 0 {
			o.maxBudgetUSD = &n[0]
		}
	}
}

// WithDisallowedTools blocks the named tools, sent as
// --disallowedTools <a,b,c>. Only sent when non-empty.
func WithDisallowedTools(tools ...string) Option {
	return func(o *options) { o.disallowedTools = tools }
}

// WithTaskBudget sets an API-side token budget, sent as
// --task-budget <n>. Variadic so an explicit 0 is distinguishable from
// unset.
func WithTaskBudget(n ...int) Option {
	return func(o *options) {
		if len(n) > 0 {
			o.taskBudget = &n[0]
		}
	}
}

// WithModel selects the model, sent as --model <name>. Only sent when
// non-empty.
func WithModel(name string) Option {
	return func(o *options) { o.model = name }
}

// WithFallbackModel sets the fallback model, sent as
// --fallback-model <name>. Only sent when non-empty.
func WithFallbackModel(name string) Option {
	return func(o *options) { o.fallbackModel = name }
}

// WithBetas opts into named beta behaviors, sent as --betas <a,b,c>. Only
// sent when non-empty.
func WithBetas(betas ...string) Option {
	return func(o *options) { o.betas = betas }
}

// WithPermissionPromptTool names a tool that handles permission prompts,
// sent as --permission-prompt-tool <name>. Only sent when non-empty.
func WithPermissionPromptTool(name string) Option {
	return func(o *options) { o.permissionPromptTool = name }
}

// WithSettings passes settings through to the CLI, sent as
// --settings <json-or-path>. Raw string passthrough: the value is either a
// settings JSON string or a path to a settings file; the CLI
// distinguishes them itself. Only sent when non-empty.
func WithSettings(settings string) Option {
	return func(o *options) { o.settings = settings }
}

// WithAddDirs grants the CLI access to additional working directories,
// sending --add-dir <dir> once per entry.
func WithAddDirs(dirs ...string) Option {
	return func(o *options) { o.addDirs = dirs }
}

// WithMCPConfig configures external MCP servers (stdio/sse/http), sent as
// --mcp-config <json-or-path>. Raw string passthrough: either a JSON
// string of the {"mcpServers": {...}} shape or a path to an MCP config
// file. In-process SDK servers (Python's type "sdk") are not supported
// here.
func WithMCPConfig(config string) Option {
	return func(o *options) { o.mcpConfig = config }
}

// WithSDKMcpServer registers an in-process MCP server whose tools the
// CLI's model can call, tunneled as mcp_message control requests. At New()
// time the server contributes a {"type":"sdk","name":...} entry to
// --mcp-config. Registering two servers with the same name is a New()
// error, not a silent overwrite. When combined with an inline-JSON
// WithMCPConfig value the two mcpServers maps are merged (SDK entries win
// on name collision); a file-path WithMCPConfig value cannot be merged
// with and errors at New() time.
func WithSDKMcpServer(server *SdkMcpServer) Option {
	return func(o *options) { o.sdkMcpServerList = append(o.sdkMcpServerList, server) }
}

// WithIncludePartialMessages enables partial message streaming
// (assistant deltas), adding --include-partial-messages. Absent by
// default.
func WithIncludePartialMessages() Option {
	return func(o *options) { o.includePartial = true }
}

// WithIncludeHookEvents surfaces hook lifecycle events in the message
// stream, adding --include-hook-events. Absent by default.
func WithIncludeHookEvents() Option {
	return func(o *options) { o.includeHookEvents = true }
}

// WithStrictMCPConfig restricts MCP servers to those in --mcp-config,
// ignoring user/project config files, adding --strict-mcp-config. Absent
// by default.
func WithStrictMCPConfig() Option {
	return func(o *options) { o.strictMcpConfig = true }
}

// WithSettingSources restricts which settings sources the CLI loads,
// sent as --setting-sources=<a,b,c> (equals-joined so the value can never
// be parsed as a separate flag). Not calling this option omits the flag
// entirely -- the CLI's own default applies.
func WithSettingSources(sources ...string) Option {
	return func(o *options) { o.settingSources = sources }
}

// WithPluginDirs loads local plugins, sending --plugin-dir <path> once per
// entry.
func WithPluginDirs(dirs ...string) Option {
	return func(o *options) { o.pluginDirs = dirs }
}

// WithExtraArgs passes raw flags through to the CLI. Each map entry sends
// --<key> alone when its value is nil (a boolean flag) or
// --<key> <value> (two tokens) otherwise -- except that a dash-leading
// value uses the equals form --<key>=<value> so it can never be parsed as
// a separate flag, mirroring the Python SDK's argv-injection guard.
func WithExtraArgs(args map[string]*string) Option {
	return func(o *options) { o.extraArgs = args }
}

// WithAdaptiveThinking enables adaptive thinking, sending
// --thinking adaptive.
func WithAdaptiveThinking() Option {
	return func(o *options) { o.thinking = &thinkingConfig{kind: thinkingAdaptive} }
}

// WithAdaptiveThinkingAndDisplay enables adaptive thinking plus a thinking
// display mode, sending --thinking adaptive --thinking-display <mode>.
func WithAdaptiveThinkingAndDisplay(display string) Option {
	return func(o *options) {
		o.thinking = &thinkingConfig{kind: thinkingAdaptive, display: display}
	}
}

// WithThinkingBudget enables thinking with a fixed token budget, sending
// --max-thinking-tokens <n>.
func WithThinkingBudget(n int) Option {
	return func(o *options) {
		o.thinking = &thinkingConfig{kind: thinkingEnabled, budgetTokens: n}
	}
}

// WithThinkingBudgetAndDisplay enables thinking with a fixed token budget
// plus a display mode, sending --max-thinking-tokens <n>
// --thinking-display <mode>.
func WithThinkingBudgetAndDisplay(n int, display string) Option {
	return func(o *options) {
		o.thinking = &thinkingConfig{kind: thinkingEnabled, budgetTokens: n, display: display}
	}
}

// WithDisabledThinking turns thinking off entirely, sending
// --thinking disabled.
func WithDisabledThinking() Option {
	return func(o *options) { o.thinking = &thinkingConfig{kind: thinkingDisabled} }
}

// WithMaxThinkingTokens sets the deprecated standalone thinking budget,
// sending --max-thinking-tokens <n>. Ignored when a With*Thinking* option
// is also set -- the explicit thinking config takes precedence, matching
// the Python SDK.
func WithMaxThinkingTokens(n int) Option {
	return func(o *options) { o.maxThinkingTokens = &n }
}

// WithEffort sets the effort level, sent as --effort <level>. Values are
// one of low/medium/high/xhigh/max but are passed through unvalidated.
// Only sent when non-empty.
func WithEffort(level string) Option {
	return func(o *options) { o.effort = level }
}

// WithJSONSchema requests structured output conforming to the given JSON
// schema, sent as --json-schema <json>. Raw string passthrough.
func WithJSONSchema(schema string) Option {
	return func(o *options) { o.jsonSchema = schema }
}

// WithEnv sets additional environment variables on the CLI subprocess,
// layered on top of the inherited environment (last-wins). The inherited
// CLAUDECODE variable is always stripped, and CLAUDE_CODE_ENTRYPOINT is
// set to "sdk-go" unless the supplied vars already set it. Entries use
// the KEY=VALUE form of os.Environ; later entries win over earlier ones.
func WithEnv(env ...string) Option {
	return func(o *options) { o.env = env }
}

// WithHooks registers hook callbacks per HookEvent. Matchers registered on
// the same event fire concurrently when the CLI invokes more than one for
// that event -- callback dispatch already runs each inbound control
// request in its own goroutine, so no additional concurrency handling is
// needed here.
func WithHooks(hooks map[HookEvent][]HookMatcher) Option {
	return func(o *options) { o.hooks = hooks }
}

// WithContinueConversation resumes the most recent conversation in the
// CLI's local session store, adding --continue.
func WithContinueConversation() Option {
	return func(o *options) { o.continueConversation = true }
}

// WithResume resumes a specific local session by ID, sent as
// --resume=<sessionID> (equals-joined). No client-side validation --
// malformed values are the CLI's problem to reject, matching the Python
// reference's flag-construction layer.
func WithResume(sessionID string) Option {
	return func(o *options) { o.resume = sessionID }
}

// WithSessionID pins the CLI-created session ID, sent as
// --session-id=<sessionID> (equals-joined).
func WithSessionID(sessionID string) Option {
	return func(o *options) { o.sessionID = sessionID }
}

// WithForkSession forks the resumed session instead of continuing it in
// place, adding --fork-session.
func WithForkSession() Option {
	return func(o *options) { o.forkSession = true }
}

// WithResumeSessionAt resumes a session at a specific transcript entry,
// sent as --resume-session-at=<entryUUID> (equals-joined).
func WithResumeSessionAt(entryUUID string) Option {
	return func(o *options) { o.resumeSessionAt = entryUUID }
}

// WithResumeDropsTurn drops a turn when resuming, sent as
// --resume-drops-turn=<turnUUID> (equals-joined). Pointer-backed so an
// explicitly-empty argument still emits --resume-drops-turn= -- Python's
// condition is "is not None", not truthiness.
func WithResumeDropsTurn(turnUUID string) Option {
	return func(o *options) { o.resumeDropsTurn = &turnUUID }
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
//
// A single read-loop goroutine (started in New) owns the CLI's stdout for
// the Client's lifetime: it resolves outbound control requests, dispatches
// inbound ones onto per-request handler goroutines, and forwards every other
// message onto the persistent stream exposed by ReceiveMessages.
type Client struct {
	tr               *transport
	permissionPolicy PermissionPolicy
	closeGracePeriod time.Duration
	controlTimeout   time.Duration

	msgs    chan Message
	closing chan struct{}

	closeOnce sync.Once

	pending   map[string]*pendingEntry
	pendingMu sync.Mutex

	inflight   map[string]context.CancelFunc
	inflightMu sync.Mutex
	handlerWG  sync.WaitGroup

	hookMu        sync.Mutex
	hookCallbacks map[string]HookCallback

	// sdkMcpServers is populated once in New and never mutated afterward,
	// so reads from dispatch goroutines need no lock (SdkMcpServer's own
	// mutex guards its tool map).
	sdkMcpServers map[string]*SdkMcpServer

	baseCtx    context.Context
	baseCancel context.CancelFunc
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

	sdkServers := make(map[string]*SdkMcpServer, len(o.sdkMcpServerList))
	for _, s := range o.sdkMcpServerList {
		if _, dup := sdkServers[s.name]; dup {
			return nil, fmt.Errorf("claudecode: duplicate SDK MCP server name %q", s.name)
		}
		sdkServers[s.name] = s
	}

	mcpConfig, err := resolveMCPConfig(&o)
	if err != nil {
		return nil, err
	}
	o.mcpConfig = mcpConfig

	args := buildArgs(&o)

	// WithEnv merges onto the inherited environment (see buildEnv);
	// withExtraEnv (test-only) keeps its wholesale-replace semantics.
	env := o.extraEnv
	if o.env != nil {
		env = buildEnv(o.env)
	}
	tr, err := startTransport(worktreePath, o.cliPath, args, env, o.logWriter)
	if err != nil {
		return nil, err
	}

	baseCtx, baseCancel := context.WithCancel(context.Background())
	c := &Client{
		tr:               tr,
		permissionPolicy: o.permissionPolicy,
		closeGracePeriod: o.closeGracePeriod,
		controlTimeout:   defaultControlTimeout,
		msgs:             make(chan Message, 100),
		closing:          make(chan struct{}),
		pending:          make(map[string]*pendingEntry),
		inflight:         make(map[string]context.CancelFunc),
		hookCallbacks:    make(map[string]HookCallback),
		sdkMcpServers:    sdkServers,
		baseCtx:          baseCtx,
		baseCancel:       baseCancel,
	}
	go c.readLoop()

	// The control channel must be initialized before the CLI accepts any
	// prompt; a failed handshake is a construction failure. Registered hook
	// callbacks are minted hook_<n> IDs here (single-threaded: the read
	// loop can't receive a hook_callback before initialize completes) and
	// only the IDs travel on the wire -- the callbacks stay Go-side, looked
	// up by ID when hook_callback requests arrive.
	var initExtra map[string]any
	if len(o.hooks) > 0 {
		hooksPayload := map[string][]map[string]any{}
		counter := 0
		for event, matchers := range o.hooks {
			for _, m := range matchers {
				var ids []string
				for _, cb := range m.Hooks {
					id := fmt.Sprintf("hook_%d", counter)
					counter++
					c.hookMu.Lock()
					c.hookCallbacks[id] = cb
					c.hookMu.Unlock()
					ids = append(ids, id)
				}
				entry := map[string]any{"matcher": m.Matcher, "hookCallbackIds": ids}
				if m.Timeout > 0 {
					entry["timeout"] = m.Timeout.Seconds()
				}
				hooksPayload[string(event)] = append(hooksPayload[string(event)], entry)
			}
		}
		initExtra = map[string]any{"hooks": hooksPayload}
	}
	if _, err := c.sendControlRequest(context.Background(), "initialize", initExtra); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("claudecode: initialize: %w", err)
	}
	return c, nil
}

// buildArgs constructs the CLI argv, mirroring the Python SDK's
// _build_command: the always-present stream-json flags and permission mode,
// then each optionally-configured flag in the reference's order. Options
// left unset produce no flag at all.
func buildArgs(o *options) []string {
	args := []string{
		"--output-format", "stream-json",
		"--verbose",
	}

	if sp := o.systemPrompt; sp == nil {
		args = append(args, "--system-prompt", "")
	} else {
		switch sp.kind {
		case systemPromptPlain:
			args = append(args, "--system-prompt", sp.value)
		case systemPromptFile:
			args = append(args, "--system-prompt-file", sp.value)
		case systemPromptPresetAppend:
			args = append(args, "--append-system-prompt", sp.append)
		}
	}

	if t := o.tools; t != nil {
		switch t.kind {
		case toolsPreset:
			args = append(args, "--tools", "default")
		case toolsList:
			args = append(args, "--tools", strings.Join(t.list, ","))
		}
	}

	if len(o.allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(o.allowedTools, ","))
	}
	if o.maxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(o.maxTurns))
	}
	if o.maxBudgetUSD != nil {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(*o.maxBudgetUSD, 'f', -1, 64))
	}
	if len(o.disallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(o.disallowedTools, ","))
	}
	if o.taskBudget != nil {
		args = append(args, "--task-budget", strconv.Itoa(*o.taskBudget))
	}
	if o.model != "" {
		args = append(args, "--model", o.model)
	}
	if o.fallbackModel != "" {
		args = append(args, "--fallback-model", o.fallbackModel)
	}
	if len(o.betas) > 0 {
		args = append(args, "--betas", strings.Join(o.betas, ","))
	}
	if o.permissionPromptTool != "" {
		args = append(args, "--permission-prompt-tool", o.permissionPromptTool)
	}

	args = append(args, "--permission-mode", o.permissionMode)

	if o.settings != "" {
		args = append(args, "--settings", o.settings)
	}
	for _, dir := range o.addDirs {
		args = append(args, "--add-dir", dir)
	}
	if o.mcpConfig != "" {
		args = append(args, "--mcp-config", o.mcpConfig)
	}
	if o.includePartial {
		args = append(args, "--include-partial-messages")
	}
	if o.includeHookEvents {
		args = append(args, "--include-hook-events")
	}
	if o.strictMcpConfig {
		args = append(args, "--strict-mcp-config")
	}
	if o.settingSources != nil {
		// Equals form so a source name can never be parsed as a separate
		// flag, mirroring the Python SDK's guard.
		args = append(args, "--setting-sources="+strings.Join(o.settingSources, ","))
	}
	for _, dir := range o.pluginDirs {
		args = append(args, "--plugin-dir", dir)
	}

	for flag, value := range o.extraArgs {
		switch {
		case value == nil:
			args = append(args, "--"+flag)
		case strings.HasPrefix(*value, "-"):
			args = append(args, "--"+flag+"="+*value)
		default:
			args = append(args, "--"+flag, *value)
		}
	}

	if t := o.thinking; t != nil {
		switch t.kind {
		case thinkingAdaptive:
			args = append(args, "--thinking", "adaptive")
		case thinkingEnabled:
			args = append(args, "--max-thinking-tokens", strconv.Itoa(t.budgetTokens))
		case thinkingDisabled:
			args = append(args, "--thinking", "disabled")
		}
		if t.kind != thinkingDisabled && t.display != "" {
			args = append(args, "--thinking-display", t.display)
		}
	} else if o.maxThinkingTokens != nil {
		args = append(args, "--max-thinking-tokens", strconv.Itoa(*o.maxThinkingTokens))
	}

	if o.effort != "" {
		args = append(args, "--effort", o.effort)
	}
	if o.jsonSchema != "" {
		args = append(args, "--json-schema", o.jsonSchema)
	}

	if o.continueConversation {
		args = append(args, "--continue")
	}
	if o.resume != "" {
		args = append(args, "--resume="+o.resume)
	}
	if o.sessionID != "" {
		args = append(args, "--session-id="+o.sessionID)
	}
	if o.forkSession {
		args = append(args, "--fork-session")
	}
	if o.resumeSessionAt != "" {
		args = append(args, "--resume-session-at="+o.resumeSessionAt)
	}
	if o.resumeDropsTurn != nil {
		args = append(args, "--resume-drops-turn="+*o.resumeDropsTurn)
	}

	args = append(args, "--input-format", "stream-json")
	return args
}

type wireUserTurn struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Message   wireUserContent `json:"message"`
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
//
// Prompt is now a convenience wrapper over the persistent engine: it sends
// the turn with Query and forwards from ReceiveResponse until the turn's
// ResultMessage.
func (c *Client) Prompt(ctx context.Context, text string, updates chan<- Message) (ResultMessage, error) {
	defer close(updates)

	if err := c.Query(ctx, text); err != nil {
		return ResultMessage{}, err
	}

	for msg := range c.ReceiveResponse(ctx) {
		if res, ok := msg.(ResultMessage); ok {
			return res, nil
		}
		select {
		case updates <- msg:
		case <-ctx.Done():
			return ResultMessage{}, ctx.Err()
		}
	}
	return ResultMessage{}, fmt.Errorf("claudecode: cli exited before sending a result message")
}

// Close cancels the read loop and any in-flight inbound control-request
// handlers, force-fails pending outbound control requests, then tears down
// the CLI subprocess via the existing stdin-close -> grace period ->
// force-kill sequence. Safe to call more than once.
func (c *Client) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		close(c.closing)
		// Cancels the base context every inbound handler derives from, so
		// even handlers registered after the inflight snapshot are stopped
		// before the wait below.
		c.baseCancel()
		c.cancelAllInflightHandlers()
		c.failAllPending(errors.New("claudecode: client closed"))
		c.handlerWG.Wait()
		closeErr = c.tr.close(c.closeGracePeriod)
	})
	return closeErr
}
