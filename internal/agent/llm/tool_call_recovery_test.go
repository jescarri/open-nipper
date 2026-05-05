package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRecoverEmbeddedToolCalls_HermesJSON(t *testing.T) {
	msg := &schema.Message{
		Role: schema.Assistant,
		Content: `Let me search.
<tool_call>
{"name": "search_gmail_messages", "arguments": {"query": "unread"}}
</tool_call>`,
	}
	n := recoverEmbeddedToolCalls(msg)
	if n != 1 {
		t.Fatalf("expected 1 recovered call, got %d", n)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected ToolCalls len 1, got %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.Function.Name != "search_gmail_messages" {
		t.Errorf("name = %q, want search_gmail_messages", tc.Function.Name)
	}
	if !strings.Contains(tc.Function.Arguments, `"query"`) {
		t.Errorf("args missing query: %q", tc.Function.Arguments)
	}
	if tc.Type != "function" {
		t.Errorf("type = %q, want function", tc.Type)
	}
	if tc.ID == "" {
		t.Error("ID should be set")
	}
	if strings.Contains(msg.Content, "<tool_call>") {
		t.Errorf("Content still contains <tool_call>: %q", msg.Content)
	}
	if msg.Content != "Let me search." {
		t.Errorf("Content = %q, want %q", msg.Content, "Let me search.")
	}
}

func TestRecoverEmbeddedToolCalls_QwenXMLInReasoning(t *testing.T) {
	// Reproduces the exact failing case from the user's logs.
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "",
		ReasoningContent: `The user wants to clear their inbox. I need to use Gmail search commands to find and delete unread emails. Let me search for unread emails in Gmail using the search tool.

<tool_call>
<function=search_gmail_messages>
<parameter=query>
unread
</parameter>
</function>
</tool_call>`,
	}
	n := recoverEmbeddedToolCalls(msg)
	if n != 1 {
		t.Fatalf("expected 1 recovered call, got %d", n)
	}
	tc := msg.ToolCalls[0]
	if tc.Function.Name != "search_gmail_messages" {
		t.Errorf("name = %q", tc.Function.Name)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("args not valid JSON: %v (%q)", err, tc.Function.Arguments)
	}
	if args["query"] != "unread" {
		t.Errorf("args query = %q, want unread", args["query"])
	}
	if strings.Contains(msg.ReasoningContent, "<tool_call>") {
		t.Errorf("reasoning still contains tool_call: %q", msg.ReasoningContent)
	}
}

func TestRecoverEmbeddedToolCalls_FunctionCallAlias(t *testing.T) {
	msg := &schema.Message{
		Content: `<function_call>{"name":"get_weather","arguments":{}}</function_call>`,
	}
	n := recoverEmbeddedToolCalls(msg)
	if n != 1 {
		t.Fatalf("expected 1 call, got %d", n)
	}
	if msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("name = %q", msg.ToolCalls[0].Function.Name)
	}
	if msg.ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("args = %q, want {}", msg.ToolCalls[0].Function.Arguments)
	}
}

func TestRecoverEmbeddedToolCalls_MultipleCalls(t *testing.T) {
	msg := &schema.Message{
		Content: `Plan:
<tool_call>{"name":"a","arguments":{"x":1}}</tool_call>
then
<tool_call>{"name":"b","arguments":{}}</tool_call>
done.`,
	}
	n := recoverEmbeddedToolCalls(msg)
	if n != 2 {
		t.Fatalf("expected 2 calls, got %d", n)
	}
	if msg.ToolCalls[0].Function.Name != "a" || msg.ToolCalls[1].Function.Name != "b" {
		t.Errorf("names = %q, %q", msg.ToolCalls[0].Function.Name, msg.ToolCalls[1].Function.Name)
	}
	// IDs should be unique.
	if msg.ToolCalls[0].ID == msg.ToolCalls[1].ID {
		t.Error("IDs should be unique across recovered calls")
	}
}

func TestRecoverEmbeddedToolCalls_StringEncodedArgs(t *testing.T) {
	// Some models emit arguments as a JSON-string-encoded JSON object.
	msg := &schema.Message{
		Content: `<tool_call>{"name":"a","arguments":"{\"k\":\"v\"}"}</tool_call>`,
	}
	if recoverEmbeddedToolCalls(msg) != 1 {
		t.Fatal("expected 1 call")
	}
	if got := msg.ToolCalls[0].Function.Arguments; got != `{"k":"v"}` {
		t.Errorf("args = %q, want unescaped object", got)
	}
}

func TestRecoverEmbeddedToolCalls_NullArgs(t *testing.T) {
	msg := &schema.Message{
		Content: `<tool_call>{"name":"a","arguments":null}</tool_call>`,
	}
	if recoverEmbeddedToolCalls(msg) != 1 {
		t.Fatal("expected 1 call")
	}
	if got := msg.ToolCalls[0].Function.Arguments; got != "{}" {
		t.Errorf("args = %q, want {}", got)
	}
}

func TestRecoverEmbeddedToolCalls_NoBlocksNoChange(t *testing.T) {
	original := "Just some text reply with no tool calls."
	msg := &schema.Message{Content: original}
	if recoverEmbeddedToolCalls(msg) != 0 {
		t.Fatal("expected 0 calls")
	}
	if msg.Content != original {
		t.Errorf("content modified: %q", msg.Content)
	}
}

func TestRecoverEmbeddedToolCalls_UnparseableBlockIsPreserved(t *testing.T) {
	// Garbage inside the block — keep it as-is so it remains visible for debugging.
	msg := &schema.Message{Content: "before <tool_call>not json, not xml</tool_call> after"}
	if recoverEmbeddedToolCalls(msg) != 0 {
		t.Fatal("expected 0 calls")
	}
	if !strings.Contains(msg.Content, "<tool_call>") {
		t.Errorf("expected unparseable block to be preserved, got %q", msg.Content)
	}
}

func TestRecoverEmbeddedToolCalls_AppendsToExistingToolCalls(t *testing.T) {
	msg := &schema.Message{
		ToolCalls: []schema.ToolCall{
			{ID: "existing", Type: "function", Function: schema.FunctionCall{Name: "preexisting", Arguments: "{}"}},
		},
		Content: `<tool_call>{"name":"recovered","arguments":{}}</tool_call>`,
	}
	if recoverEmbeddedToolCalls(msg) != 1 {
		t.Fatal("expected 1 recovery")
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls total, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "preexisting" {
		t.Error("preexisting call should remain first")
	}
	if msg.ToolCalls[1].Function.Name != "recovered" {
		t.Error("recovered call should be appended")
	}
}

func TestRecoverEmbeddedToolCalls_NilMessage(t *testing.T) {
	if recoverEmbeddedToolCalls(nil) != 0 {
		t.Fatal("nil message should return 0")
	}
}

func TestNormalizeToolCalls_RecoversAndDefaultsArgs(t *testing.T) {
	// Existing call with empty args should be defaulted to "{}".
	// Recovered call should be added.
	msg := &schema.Message{
		ToolCalls: []schema.ToolCall{
			{ID: "1", Type: "function", Function: schema.FunctionCall{Name: "x", Arguments: ""}},
		},
		Content: `<tool_call>{"name":"y","arguments":{"a":1}}</tool_call>`,
	}
	normalizeToolCalls(msg)
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("empty args should be defaulted to {}, got %q", msg.ToolCalls[0].Function.Arguments)
	}
	if msg.ToolCalls[1].Function.Name != "y" {
		t.Errorf("recovered call missing")
	}
}
