package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func jsonEqual(t *testing.T, got, want string) {
	t.Helper()

	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got not JSON: %q (%v)", got, err)
	}

	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want not JSON: %q (%v)", want, err)
	}

	gj, _ := json.Marshal(g)

	wj, _ := json.Marshal(w)
	if string(gj) != string(wj) {
		t.Errorf("JSON mismatch:\n  got  %s\n  want %s", gj, wj)
	}
}

func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}

		if after, ok := strings.CutPrefix(a, flag+"="); ok {
			return after, true
		}
	}

	return "", false
}

func TestClient_WithAgentsInitializePayload(t *testing.T) {
	t.Parallel()

	agents := map[string]AgentDefinition{
		"code-reviewer": {
			Description:     "Reviews code",
			Prompt:          "Review diffs",
			Model:           "claude-sonnet-4",
			MaxTurns:        3,
			PermissionMode:  "acceptEdits",
			DisallowedTools: []string{"Bash"},
		},
		"minimal": {}, // all zero values: every field omitted
	}

	c, err := New(t.TempDir(), append(fakeCLIEnvOpts(t, "capture_stdin",
		"CLAUDECODE_FAKE_MAX_LINES=1", "CLAUDECODE_FAKE_RECORD_INIT=1"),
		WithAgents(agents))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// RECORD_INIT=1 makes the scenario record-and-count the initialize
	// request itself; MAX_LINES=1 means it dumps immediately after acking
	// initialize, no further traffic needed.

	var captured []string

	for msg := range c.ReceiveResponse(context.Background()) {
		if res, ok := msg.(ResultMessage); ok {
			if err := json.Unmarshal([]byte(res.Result), &captured); err != nil {
				t.Fatalf("unmarshal captured stdin: %v", err)
			}

			break
		}
	}

	_ = c.Close()

	var init struct {
		Request struct {
			Agents map[string]json.RawMessage `json:"agents"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(captured[0]), &init); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}

	if len(init.Request.Agents) != 2 {
		t.Fatalf("agents = %s", init.Request.Agents)
	}

	jsonEqual(t, string(init.Request.Agents["code-reviewer"]), `{
		"description": "Reviews code",
		"prompt": "Review diffs",
		"disallowedTools": ["Bash"],
		"model": "claude-sonnet-4",
		"maxTurns": 3,
		"permissionMode": "acceptEdits"
	}`)

	// All-zero agent: no field may appear on the wire (not even "null"s).
	var m map[string]any
	if err := json.Unmarshal(init.Request.Agents["minimal"], &m); err != nil {
		t.Fatalf("unmarshal minimal: %v", err)
	}

	if len(m) != 0 {
		t.Errorf("zero-value agent leaked fields: %s", init.Request.Agents["minimal"])
	}
}

func TestClient_WithAgentsUnsetOmitsKey(t *testing.T) {
	t.Parallel()

	// ackInitialize acks the initialize request regardless, so plain
	// capture suffices to prove New succeeds without agents.
	c, err := New(t.TempDir(), fakeCLIOptions(t, "capture")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = c.Close() }()

	updates := make(chan Message)
	if _, err := c.Prompt(context.Background(), "hi", updates); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
}

func TestClient_WithSkillsInitializePayload(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), append(fakeCLIEnvOpts(t, "capture_stdin",
		"CLAUDECODE_FAKE_MAX_LINES=1", "CLAUDECODE_FAKE_RECORD_INIT=1"),
		WithSkills("alpha", "beta"))...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var captured []string

	for msg := range c.ReceiveResponse(context.Background()) {
		if res, ok := msg.(ResultMessage); ok {
			if err := json.Unmarshal([]byte(res.Result), &captured); err != nil {
				t.Fatalf("unmarshal captured stdin: %v", err)
			}

			break
		}
	}

	_ = c.Close()

	var init struct {
		Request struct {
			Skills  []string       `json:"skills"`
			Agents  map[string]any `json:"agents"`
			Unknown map[string]any `json:"-"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(captured[0]), &init); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}

	if len(init.Request.Skills) != 2 || init.Request.Skills[0] != "alpha" || init.Request.Skills[1] != "beta" {
		t.Errorf("skills = %v, want [alpha beta]", init.Request.Skills)
	}
}

func TestClient_WithAllSkillsOmitsSkillsKey(t *testing.T) {
	t.Parallel()

	c, err := New(t.TempDir(), append(fakeCLIEnvOpts(t, "capture_stdin",
		"CLAUDECODE_FAKE_MAX_LINES=1", "CLAUDECODE_FAKE_RECORD_INIT=1"),
		WithAllSkills())...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var captured []string

	for msg := range c.ReceiveResponse(context.Background()) {
		if res, ok := msg.(ResultMessage); ok {
			if err := json.Unmarshal([]byte(res.Result), &captured); err != nil {
				t.Fatalf("unmarshal captured stdin: %v", err)
			}

			break
		}
	}

	_ = c.Close()

	var init struct {
		Request map[string]json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal([]byte(captured[0]), &init); err != nil {
		t.Fatalf("unmarshal initialize: %v", err)
	}

	if _, has := init.Request["skills"]; has {
		t.Errorf("WithAllSkills must not send a skills key: %s", captured[0])
	}
	// But the defaulting side effects still fired -- see the flags tests.
}

func TestClient_SkillsAllowedToolsUnion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       []Option
		wantJoined string
	}{
		{
			name:       "all skills adds Skill",
			opts:       []Option{WithAllSkills()},
			wantJoined: "Skill",
		},
		{
			name:       "named skills add Skill(name) entries",
			opts:       []Option{WithSkills("alpha", "beta")},
			wantJoined: "Skill(alpha),Skill(beta)",
		},
		{
			name:       "union with explicit entries",
			opts:       []Option{WithAllowedTools("Read", "Skill(alpha)"), WithSkills("alpha", "beta")},
			wantJoined: "Read,Skill(alpha),Skill(beta)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"), tt.opts...))

			got, ok := flagValue(args, "--allowedTools")
			if !ok {
				t.Fatalf("no --allowedTools in argv: %v", args)
			}

			if got != tt.wantJoined {
				t.Errorf("--allowedTools = %q, want %q", got, tt.wantJoined)
			}
		})
	}
}

func TestClient_SkillsSettingSourcesDefaulting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []Option
		want string // "" = flag absent
	}{
		{name: "skills default setting sources", opts: []Option{WithSkills("alpha")}, want: "user,project"},
		{name: "all skills default setting sources", opts: []Option{WithAllSkills()}, want: "user,project"},
		{name: "explicit setting sources not overridden", opts: []Option{WithSkills("alpha"), WithSettingSources("user")}, want: "user"},
		{
			// An explicit zero-arg call still counts as "called": the
			// flag is emitted (empty value, matching WithSettingSources's
			// pre-existing behavior) and skills do not default it.
			name: "explicit empty setting sources not overridden",
			opts: []Option{WithSkills("alpha"), WithSettingSources()},
			want: "=",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"), tt.opts...))
			if tt.want == "" {
				if _, ok := flagValue(args, "--setting-sources"); ok {
					t.Errorf("--setting-sources unexpectedly present")
				}

				return
			}

			got, ok := flagValue(args, "--setting-sources")
			if !ok {
				t.Fatalf("no --setting-sources in argv: %v", args)
			}

			want := strings.TrimPrefix(tt.want, "=")
			if got != want {
				t.Errorf("--setting-sources = %q, want %q", got, want)
			}
		})
	}
}

func TestClient_SandboxSettingsJSON(t *testing.T) {
	t.Parallel()

	s := SandboxSettings{} // every pointer nil, every slice nil

	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if string(raw) != "{}" {
		t.Errorf("empty sandbox marshaled to %s, want {}", raw)
	}

	net := SandboxNetworkConfig{}

	rawN, _ := json.Marshal(net)
	if string(rawN) != "{}" {
		t.Errorf("empty network config marshaled to %s, want {}", rawN)
	}

	ig := SandboxIgnoreViolations{}

	rawI, _ := json.Marshal(ig)
	if string(rawI) != "{}" {
		t.Errorf("empty ignoreViolations marshaled to %s, want {}", rawI)
	}
}

func TestClient_WithSandboxAlone(t *testing.T) {
	t.Parallel()

	args, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"),
		WithSandbox(SandboxSettings{Enabled: new(true)})))

	got, ok := flagValue(args, "--settings")
	if !ok {
		t.Fatalf("no --settings in argv: %v", args)
	}

	jsonEqual(t, got, `{"sandbox":{"enabled":true}}`)
}

func TestClient_WithSandboxMergesWithSettings(t *testing.T) {
	t.Parallel()

	network := SandboxNetworkConfig{
		AllowedDomains: []string{"example.com"},
		HTTPProxyPort:  new(8080),
	}
	args, _ := captureSpawn(t, append(fakeCLIOptions(t, "capture"),
		WithSettings(`{"foo":1,"sandbox":{"enabled":false}}`),
		WithSandbox(SandboxSettings{
			Enabled:                   new(true),
			ExcludedCommands:          []string{"rm"},
			Network:                   &network,
			AllowUnsandboxedCommands:  new(false),
			IgnoreViolations:          &SandboxIgnoreViolations{Network: []string{"n1"}},
			EnableWeakerNestedSandbox: new(true),
		})))

	got, ok := flagValue(args, "--settings")
	if !ok {
		t.Fatalf("no --settings in argv: %v", args)
	}

	jsonEqual(t, got, `{
		"foo": 1,
		"sandbox": {
			"enabled": true,
			"excludedCommands": ["rm"],
			"allowUnsandboxedCommands": false,
			"network": {
				"allowedDomains": ["example.com"],
				"httpProxyPort": 8080
			},
			"ignoreViolations": {"network": ["n1"]},
			"enableWeakerNestedSandbox": true
		}
	}`)
}

func TestClient_WithSandboxNonJSONSettingsError(t *testing.T) {
	t.Parallel()

	_, err := New(t.TempDir(), append(fakeCLIOptions(t, "capture"),
		WithSettings("/tmp/settings.json"),
		WithSandbox(SandboxSettings{Enabled: new(true)}))...)
	if err == nil || !strings.Contains(err.Error(), "cannot be merged with WithSandbox") {
		t.Fatalf("non-JSON settings + sandbox error = %v", err)
	}
}

func TestClient_EnableFileCheckpointingEnv(t *testing.T) {
	// Not parallel: t.Setenv mutates process env.
	t.Setenv("CLAUDECODE", "1")

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	base := []Option{
		WithCLIPath(self),
		WithEnableFileCheckpointing(),
		WithEnv(
			"CLAUDECODE_FAKE_CLI=1",
			"CLAUDECODE_FAKE_SCENARIO=capture",
		),
	}

	_, gotEnv := captureSpawn(t, base)

	envMap := envMapOf(gotEnv)
	if envMap["CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING"] != "true" {
		t.Errorf("CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING = %q, want true", envMap["CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING"])
	}

	// Caller-supplied WithEnv value for the same key wins.
	_, gotEnv2 := captureSpawn(t, append(base,
		WithEnv(
			"CLAUDECODE_FAKE_CLI=1",
			"CLAUDECODE_FAKE_SCENARIO=capture",
			"CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING=custom",
		)))

	envMap2 := envMapOf(gotEnv2)
	if envMap2["CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING"] != "custom" {
		t.Errorf("caller override lost: %q, want custom", envMap2["CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING"])
	}
}

func envMapOf(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}

	return m
}

func TestQueryOnce_RoundTrip(t *testing.T) {
	t.Parallel()

	// Use a scenario that echoes its argv/env/pid back, so we can learn
	// the fake CLI's PID and later prove QueryOnce closed it.
	dir := t.TempDir()

	updates := make(chan Message)
	go func() {
		for range updates { //nolint:revive  // intentional drain to unblock QueryOnce's forwarding, no per-message action needed
		}
	}()

	res, err := QueryOnce(context.Background(), dir, "hi", updates, fakeCLIOptions(t, "capture")...)
	if err != nil {
		t.Fatalf("QueryOnce() error = %v", err)
	}

	var dump struct {
		Args []string `json:"args"`
		Env  []string `json:"env"`
		Pid  int      `json:"pid"`
	}
	if err := json.Unmarshal([]byte(res.Result), &dump); err != nil {
		t.Fatalf("unmarshal captured dump %q: %v", res.Result, err)
	}

	// The subprocess must actually be gone after QueryOnce returned
	// (Close ran via defer). It exited on its own right after the capture
	// turn, but the kernel reaps it only once Wait() completes, which is
	// Close's job. Signal 0 on a reaped PID fails; retry briefly in case
	// of scheduler lag, then it must be gone.
	deadline := time.Now().Add(2 * time.Second)

	for {
		err := syscall.Kill(dump.Pid, 0)
		if err != nil {
			break // process no longer running: closed and reaped
		}

		if time.Now().After(deadline) {
			t.Fatalf("fake CLI pid %d still running after QueryOnce returned", dump.Pid)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestQueryOnce_NewErrorPropagates(t *testing.T) {
	t.Parallel()

	updates := make(chan Message, 1)

	_, err := QueryOnce(context.Background(), t.TempDir(), "hi", updates,
		WithCLIPath("/nonexistent/claude-binary"))
	if err == nil {
		t.Fatal("QueryOnce() with bad CLI path returned nil error")
	}

	if _, ok := <-updates; ok {
		t.Error("updates channel not closed on New failure")
	}
}
