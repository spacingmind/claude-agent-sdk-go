package claudecode

import (
	"encoding/json"
	"testing"
)

func marshalUpdate(t *testing.T, u PermissionUpdate) map[string]any {
	t.Helper()

	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}

	return m
}

func assertKeys(t *testing.T, m map[string]any, want map[string]any, absent ...string) {
	t.Helper()

	for k, v := range want {
		if m[k] != v {
			t.Fatalf("[%q] = %#v, want %#v (full: %v)", k, m[k], v, m)
		}
	}

	for _, k := range absent {
		if _, present := m[k]; present {
			t.Fatalf("unexpectedly contains %q: %v", k, m)
		}
	}
}

func TestPermissionUpdateMarshalRulesVariants(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{"addRules", "replaceRules", "removeRules"} {
		u := PermissionUpdate{
			Type:        typ,
			Rules:       []PermissionRuleValue{{ToolName: "Bash", RuleContent: "git status"}},
			Behavior:    "allow",
			Mode:        "plan",                   // irrelevant
			Directories: []string{"/should-drop"}, // irrelevant
		}

		m := marshalUpdate(t, u)
		assertKeys(t, m, map[string]any{"type": typ, "behavior": "allow"}, "mode", "directories", "destination", "ruleContent")

		rules, _ := m["rules"].([]any)
		if len(rules) != 1 {
			t.Fatalf("[%s] rules = %#v, want one entry", typ, m["rules"])
		}

		r, _ := rules[0].(map[string]any)
		assertKeys(t, r, map[string]any{"toolName": "Bash", "ruleContent": "git status"})
	}
}

func TestPermissionUpdateMarshalRuleContentOmitted(t *testing.T) {
	t.Parallel()

	m := marshalUpdate(t, PermissionUpdate{
		Type:     "addRules",
		Rules:    []PermissionRuleValue{{ToolName: "WebFetch"}},
		Behavior: "ask",
	})

	rules, _ := m["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules = %#v, want one entry", m["rules"])
	}

	r, _ := rules[0].(map[string]any)
	assertKeys(t, r, map[string]any{"toolName": "WebFetch"}, "ruleContent")
}

func TestPermissionUpdateMarshalSetMode(t *testing.T) {
	t.Parallel()

	u := PermissionUpdate{
		Type:        "setMode",
		Mode:        "plan",
		Rules:       []PermissionRuleValue{{ToolName: "x"}}, // irrelevant
		Behavior:    "deny",                                 // irrelevant
		Directories: []string{"/drop"},                      // irrelevant
	}

	m := marshalUpdate(t, u)
	assertKeys(t, m, map[string]any{"type": "setMode", "mode": "plan"}, "rules", "behavior", "directories", "destination")
}

func TestPermissionUpdateMarshalDirectoriesVariants(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{"addDirectories", "removeDirectories"} {
		u := PermissionUpdate{
			Type:        typ,
			Directories: []string{"/a", "/b"},
			Mode:        "plan",                                 // irrelevant
			Behavior:    "allow",                                // irrelevant
			Rules:       []PermissionRuleValue{{ToolName: "x"}}, // irrelevant
		}

		m := marshalUpdate(t, u)
		assertKeys(t, m, map[string]any{"type": typ}, "mode", "behavior", "rules", "destination")

		dirs, errJSON := m["directories"].([]any)
		if !errJSON || len(dirs) != 2 || dirs[0] != "/a" || dirs[1] != "/b" {
			t.Fatalf("[%s] directories = %#v", typ, m["directories"])
		}
	}
}

func TestPermissionUpdateMarshalDestination(t *testing.T) {
	t.Parallel()

	cases := []PermissionUpdate{
		{Type: "addRules", Rules: []PermissionRuleValue{{ToolName: "Bash"}}, Behavior: "deny", Destination: "userSettings"},
		{Type: "setMode", Mode: "acceptEdits", Destination: "localSettings"},
		{Type: "removeDirectories", Directories: []string{"/tmp"}, Destination: "session"},
	}

	for _, u := range cases {
		m := marshalUpdate(t, u)
		if m["destination"] != u.Destination {
			t.Fatalf("destination = %#v, want %q (full: %v)", m["destination"], u.Destination, m)
		}
	}

	noDest := marshalUpdate(t, PermissionUpdate{Type: "setMode", Mode: "default"})
	assertKeys(t, noDest, map[string]any{"type": "setMode", "mode": "default"}, "destination")
}

func TestPermissionUpdateMarshalUnknownType(t *testing.T) {
	t.Parallel()

	u := PermissionUpdate{Type: "bogus", Mode: "plan", Rules: []PermissionRuleValue{{ToolName: "x"}}, Destination: "session"}

	m := marshalUpdate(t, u)
	assertKeys(t, m, map[string]any{"type": "bogus", "destination": "session"}, "mode", "rules", "behavior", "directories")
}
