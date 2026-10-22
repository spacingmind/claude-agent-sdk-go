package claudecode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// promptEchoLine runs prompt against the echo_prompt fake CLI and returns the
// user-turn line the CLI received on stdin.
func promptEchoLine(t *testing.T, prompt func(c *Client, updates chan<- Message) (ResultMessage, error)) string {
	t.Helper()

	c, err := New(t.TempDir(), fakeCLIOptions(t, "echo_prompt")...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	defer func() { _ = c.Close() }()

	updates := make(chan Message)

	go func() {
		for range updates { //nolint:revive // drain
		}
	}()

	res, err := prompt(c, updates)
	if err != nil {
		t.Fatalf("prompt error = %v", err)
	}

	return res.Result
}

func TestPromptBlocks_WritesContentArray(t *testing.T) {
	t.Parallel()

	line := promptEchoLine(t, func(c *Client, updates chan<- Message) (ResultMessage, error) {
		return c.PromptBlocks(context.Background(), []PromptBlock{
			PromptText("what is this?"),
			PromptImage("image/png", "iVBORw0KGgo="),
		}, updates)
	})

	want := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"text","text":"what is this?"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}}`
	if line != want {
		t.Fatalf("stdin line =\n%s\nwant\n%s", line, want)
	}
}

func TestPromptBlocks_TextOnlyUnchanged(t *testing.T) {
	t.Parallel()

	// A plain Prompt still writes content as a string, byte for byte.
	line := promptEchoLine(t, func(c *Client, updates chan<- Message) (ResultMessage, error) {
		return c.Prompt(context.Background(), "hello", updates)
	})

	want := `{"type":"user","message":{"role":"user","content":"hello"}}`
	if line != want {
		t.Fatalf("stdin line = %s, want %s", line, want)
	}

	// A text-only block turn is an array with one text block.
	line = promptEchoLine(t, func(c *Client, updates chan<- Message) (ResultMessage, error) {
		return c.PromptBlocks(context.Background(), []PromptBlock{PromptText("hello")}, updates)
	})

	var turn struct {
		Message struct {
			Content []map[string]any `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &turn); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}

	if len(turn.Message.Content) != 1 || turn.Message.Content[0]["text"] != "hello" {
		t.Fatalf("content = %v, want one text block", turn.Message.Content)
	}
}

func TestQueryBlocks_RejectsMalformedBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		blocks []PromptBlock
		want   string
	}{
		{"empty", nil, "no content blocks"},
		{"unknown type", []PromptBlock{{Type: "audio"}}, `unknown type "audio"`},
		{"image without data", []PromptBlock{PromptImage("image/png", "")}, "needs a media type and data"},
		{"image without media type", []PromptBlock{PromptImage("", "AAAA")}, "needs a media type and data"},
		{"image without source", []PromptBlock{{Type: PromptBlockImage}}, "needs a media type and data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Validation runs before anything touches the transport, so a
			// zero Client is enough and no CLI is spawned.
			err := (&Client{}).QueryBlocks(context.Background(), tt.blocks)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("QueryBlocks() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
