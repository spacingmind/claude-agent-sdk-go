package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// captureSpawn runs one Prompt turn against the "capture" fake-CLI
// scenario and returns the argv and environment the client spawned it
// with. The fake CLI encodes both in the result message's text.
func captureSpawn(t *testing.T, opts []Option) (args []string, env []string) {
	t.Helper()

	c, err := New(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer c.Close()

	updates := make(chan Message)
	res, err := c.Prompt(context.Background(), "hi", updates)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var dump struct {
		Args []string `json:"args"`
		Env  []string `json:"env"`
	}
	if err := json.Unmarshal([]byte(res.Result), &dump); err != nil {
		t.Fatalf("unmarshal captured dump %q: %v", res.Result, err)
	}
	return dump.Args, dump.Env
}

func TestClient_FlagsForOptions(t *testing.T) {
	t.Parallel()

	deprecatedTokens := 4096

	// Each case's option tokens, split by where they sit in argv relative
	// to --permission-mode -- the Python reference's _build_command order.
	// sysSet marks cases that configure a system-prompt form themselves
	// (overriding the always-present unset "--system-prompt \"\"" pair);
	// perm overrides the expected --permission-mode value.
	tests := []struct {
		name   string
		opts   []Option
		pre    []string
		post   []string
		sysSet bool
		perm   string
	}{
		{name: "no options"},
		{
			name:   "system prompt plain",
			opts:   []Option{WithSystemPrompt("be terse")},
			pre:    []string{"--system-prompt", "be terse"},
			sysSet: true,
		},
		{
			name:   "system prompt file",
			opts:   []Option{WithSystemPromptFile("/tmp/sp.txt")},
			pre:    []string{"--system-prompt-file", "/tmp/sp.txt"},
			sysSet: true,
		},
		{
			name:   "system prompt preset append",
			opts:   []Option{WithAppendSystemPrompt("stay safe")},
			pre:    []string{"--append-system-prompt", "stay safe"},
			sysSet: true,
		},
		{
			name: "tools empty list",
			opts: []Option{WithTools()},
			pre:  []string{"--tools", ""},
		},
		{
			name: "tools list",
			opts: []Option{WithTools("Read", "Bash")},
			pre:  []string{"--tools", "Read,Bash"},
		},
		{
			name: "tools preset",
			opts: []Option{WithDefaultToolsPreset()},
			pre:  []string{"--tools", "default"},
		},
		{
			name: "allowed tools",
			opts: []Option{WithAllowedTools("Read", "Grep")},
			pre:  []string{"--allowedTools", "Read,Grep"},
		},
		{
			name: "max turns",
			opts: []Option{WithMaxTurns(5)},
			pre:  []string{"--max-turns", "5"},
		},
		{
			name: "max budget",
			opts: []Option{WithMaxBudgetUSD(1.5)},
			pre:  []string{"--max-budget-usd", "1.5"},
		},
		{
			name: "max budget zero is explicit",
			opts: []Option{WithMaxBudgetUSD(0)},
			pre:  []string{"--max-budget-usd", "0"},
		},
		{
			name: "disallowed tools",
			opts: []Option{WithDisallowedTools("Bash", "Write")},
			pre:  []string{"--disallowedTools", "Bash,Write"},
		},
		{
			name: "task budget",
			opts: []Option{WithTaskBudget(1000)},
			pre:  []string{"--task-budget", "1000"},
		},
		{
			name: "task budget zero is explicit",
			opts: []Option{WithTaskBudget(0)},
			pre:  []string{"--task-budget", "0"},
		},
		{
			name: "model",
			opts: []Option{WithModel("claude-sonnet-4")},
			pre:  []string{"--model", "claude-sonnet-4"},
		},
		{
			name: "fallback model",
			opts: []Option{WithFallbackModel("claude-haiku-4")},
			pre:  []string{"--fallback-model", "claude-haiku-4"},
		},
		{
			name: "betas",
			opts: []Option{WithBetas("b1", "b2")},
			pre:  []string{"--betas", "b1,b2"},
		},
		{
			name: "permission prompt tool",
			opts: []Option{WithPermissionPromptTool("my-tool")},
			pre:  []string{"--permission-prompt-tool", "my-tool"},
		},
		{
			name: "settings",
			opts: []Option{WithSettings(`{"env":{}}`)},
			post: []string{"--settings", `{"env":{}}`},
		},
		{
			name: "add dirs repeated",
			opts: []Option{WithAddDirs("/a", "/b")},
			post: []string{"--add-dir", "/a", "--add-dir", "/b"},
		},
		{
			name: "mcp config",
			opts: []Option{WithMCPConfig(`{"mcpServers":{}}`)},
			post: []string{"--mcp-config", `{"mcpServers":{}}`},
		},
		{
			name: "include partial messages",
			opts: []Option{WithIncludePartialMessages()},
			post: []string{"--include-partial-messages"},
		},
		{
			name: "include hook events",
			opts: []Option{WithIncludeHookEvents()},
			post: []string{"--include-hook-events"},
		},
		{
			name: "strict mcp config",
			opts: []Option{WithStrictMCPConfig()},
			post: []string{"--strict-mcp-config"},
		},
		{
			name: "setting sources",
			opts: []Option{WithSettingSources("user", "project")},
			post: []string{"--setting-sources=user,project"},
		},
		{
			name: "plugin dirs repeated",
			opts: []Option{WithPluginDirs("/p1", "/p2")},
			post: []string{"--plugin-dir", "/p1", "--plugin-dir", "/p2"},
		},
		{
			name: "extra args boolean",
			opts: []Option{WithExtraArgs(map[string]*string{"flat": nil})},
			post: []string{"--flat"},
		},
		{
			name: "extra args valued",
			opts: []Option{WithExtraArgs(map[string]*string{"flag": strPtr("value")})},
			post: []string{"--flag", "value"},
		},
		{
			name: "extra args dash-leading value uses equals form",
			opts: []Option{WithExtraArgs(map[string]*string{"flag": strPtr("-injected")})},
			post: []string{"--flag=-injected"},
		},
		{
			name: "thinking adaptive",
			opts: []Option{WithAdaptiveThinking()},
			post: []string{"--thinking", "adaptive"},
		},
		{
			name: "thinking adaptive with display",
			opts: []Option{WithAdaptiveThinkingAndDisplay("compact")},
			post: []string{"--thinking", "adaptive", "--thinking-display", "compact"},
		},
		{
			name: "thinking budget",
			opts: []Option{WithThinkingBudget(8192)},
			post: []string{"--max-thinking-tokens", "8192"},
		},
		{
			name: "thinking budget with display",
			opts: []Option{WithThinkingBudgetAndDisplay(8192, "always")},
			post: []string{"--max-thinking-tokens", "8192", "--thinking-display", "always"},
		},
		{
			name: "thinking disabled",
			opts: []Option{WithDisabledThinking()},
			post: []string{"--thinking", "disabled"},
		},
		{
			name: "deprecated max thinking tokens",
			opts: []Option{WithMaxThinkingTokens(deprecatedTokens)},
			post: []string{"--max-thinking-tokens", "4096"},
		},
		{
			name: "thinking config wins over deprecated max thinking tokens",
			opts: []Option{WithMaxThinkingTokens(deprecatedTokens), WithAdaptiveThinking()},
			post: []string{"--thinking", "adaptive"},
		},
		{
			name: "disabled thinking wins over deprecated max thinking tokens",
			opts: []Option{WithMaxThinkingTokens(deprecatedTokens), WithDisabledThinking()},
			post: []string{"--thinking", "disabled"},
		},
		{
			name: "effort",
			opts: []Option{WithEffort("xhigh")},
			post: []string{"--effort", "xhigh"},
		},
		{
			name: "json schema",
			opts: []Option{WithJSONSchema(`{"type":"object"}`)},
			post: []string{"--json-schema", `{"type":"object"}`},
		},
		{
			name: "permission mode override",
			opts: []Option{WithPermissionMode("acceptEdits")},
			perm: "acceptEdits",
		},
		{
			name: "continue conversation",
			opts: []Option{WithContinueConversation()},
			post: []string{"--continue"},
		},
		{
			name: "resume",
			opts: []Option{WithResume("abc-123")},
			post: []string{"--resume=abc-123"},
		},
		{
			name: "session id",
			opts: []Option{WithSessionID("sid-1")},
			post: []string{"--session-id=sid-1"},
		},
		{
			name: "fork session",
			opts: []Option{WithForkSession()},
			post: []string{"--fork-session"},
		},
		{
			name: "resume session at",
			opts: []Option{WithResumeSessionAt("uuid-1")},
			post: []string{"--resume-session-at=uuid-1"},
		},
		{
			name: "resume drops turn",
			opts: []Option{WithResumeDropsTurn("turn-1")},
			post: []string{"--resume-drops-turn=turn-1"},
		},
		{
			name: "resume drops turn empty is explicit",
			opts: []Option{WithResumeDropsTurn("")},
			post: []string{"--resume-drops-turn="},
		},
		{
			name: "resume flags combined with model",
			opts: []Option{
				WithResume("abc-123"),
				WithModel("claude-sonnet-4"),
			},
			pre:  []string{"--model", "claude-sonnet-4"},
			post: []string{"--resume=abc-123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := fakeCLIOptions(t, "capture")
			opts = append(opts, tt.opts...)
			args, _ := captureSpawn(t, opts)

			// Exact argv check: always-present flags, this case's option
			// tokens in the reference's documented order, nothing else.
			perm := tt.perm
			if perm == "" {
				perm = "default"
			}
			wantArgs := []string{"--output-format", "stream-json", "--verbose"}
			if !tt.sysSet {
				wantArgs = append(wantArgs, "--system-prompt", "")
			}
			wantArgs = append(wantArgs, tt.pre...)
			wantArgs = append(wantArgs, "--permission-mode", perm)
			wantArgs = append(wantArgs, tt.post...)
			wantArgs = append(wantArgs, "--input-format", "stream-json")
			if got, want := strings.Join(args, " "), strings.Join(wantArgs, " "); got != want {
				t.Errorf("argv =\n  %q\nwant\n  %q", got, want)
			}
		})
	}
}

func TestClient_UnsetOptionsProduceNoFlag(t *testing.T) {
	t.Parallel()

	args, _ := captureSpawn(t, fakeCLIOptions(t, "capture"))

	// No option set: only the always-present flags plus the unset
	// system-prompt's explicit empty wire form.
	want := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--system-prompt", "",
		"--permission-mode", "default",
		"--input-format", "stream-json",
	}
	if got, want := strings.Join(args, " "), strings.Join(want, " "); got != want {
		t.Fatalf("argv = %q, want %q (unset options must produce no flag at all)", got, want)
	}
}

func TestClient_ExtraArgsRoundTripRaw(t *testing.T) {
	t.Parallel()

	opts := append(fakeCLIOptions(t, "capture"),
		WithExtraArgs(map[string]*string{"weird": strPtr("raw value")}))
	args, _ := captureSpawn(t, opts)

	if !containsToken(args, "--weird") || !containsToken(args, "raw value") {
		t.Errorf("argv = %q, want --weird and 'raw value' passed through unmodified", strings.Join(args, " "))
	}
}

func TestClient_EnvMerge(t *testing.T) {
	// Not parallel: t.Setenv mutates process env.
	// CLAUDECODE present in the test runner's own (inherited) environment
	// must not reach the subprocess.
	t.Setenv("CLAUDECODE", "1")

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	opts := []Option{
		WithCLIPath(self),
		WithEnv(
			"CLAUDECODE_FAKE_CLI=1",
			"CLAUDECODE_FAKE_SCENARIO=capture",
			"MY_TEST_VAR=hello",
		),
	}

	args, gotEnv := captureSpawn(t, opts)
	if len(args) == 0 {
		t.Fatal("no args captured")
	}

	envMap := make(map[string]string, len(gotEnv))
	for _, kv := range gotEnv {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}

	if v, ok := envMap["CLAUDECODE"]; ok {
		t.Errorf("CLAUDECODE leaked into subprocess env as %q", v)
	}
	if envMap["CLAUDE_CODE_ENTRYPOINT"] != "sdk-go" {
		t.Errorf("CLAUDE_CODE_ENTRYPOINT = %q, want %q", envMap["CLAUDE_CODE_ENTRYPOINT"], "sdk-go")
	}
	if envMap["MY_TEST_VAR"] != "hello" {
		t.Errorf("MY_TEST_VAR = %q, want %q", envMap["MY_TEST_VAR"], "hello")
	}
	// Caller-supplied vars land alongside inherited vars, not replacing
	// them: PATH must survive.
	if envMap["PATH"] == "" {
		t.Error("subprocess env lost inherited PATH (env was replaced, not merged)")
	}
}

func TestClient_EnvEntrypointCallerOverrideWins(t *testing.T) {
	t.Parallel()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	opts := []Option{
		WithCLIPath(self),
		WithEnv(
			"CLAUDECODE_FAKE_CLI=1",
			"CLAUDECODE_FAKE_SCENARIO=capture",
			"CLAUDE_CODE_ENTRYPOINT=custom",
		),
	}

	_, gotEnv := captureSpawn(t, opts)

	envMap := make(map[string]string, len(gotEnv))
	for _, kv := range gotEnv {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}
	if envMap["CLAUDE_CODE_ENTRYPOINT"] != "custom" {
		t.Errorf("CLAUDE_CODE_ENTRYPOINT = %q, want custom (caller override wins)", envMap["CLAUDE_CODE_ENTRYPOINT"])
	}
}

func containsToken(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }
