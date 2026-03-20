package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
	"go.uber.org/zap"
)

type stubMCPTool struct {
	info      *schema.ToolInfo
	lastArgs  string
	returnVal string
	returnErr error
}

func (s *stubMCPTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return s.info, nil
}

func (s *stubMCPTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	s.lastArgs = argsJSON
	return s.returnVal, s.returnErr
}

func createNoteToolInfo() *schema.ToolInfo {
	props := orderedmap.New[string, *jsonschema.Schema]()
	props.Set("title", &jsonschema.Schema{Type: "string"})
	props.Set("body", &jsonschema.Schema{Type: "string"})
	props.Set("parent_id", &jsonschema.Schema{Type: "string"})
	props.Set("tags", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}})
	props.Set("tag_names", &jsonschema.Schema{Type: "array"})

	return &schema.ToolInfo{
		Name: "create_note",
		Desc: "Create a note",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Type:       "object",
			Required:   []string{"title", "parent_id", "tags"},
			Properties: props,
		}),
	}
}

func TestNormalizeArgs_InjectsEmptyArrayForMissingTags(t *testing.T) {
	inner := &stubMCPTool{
		info:      createNoteToolInfo(),
		returnVal: `{"ok":true}`,
	}
	wrapped := &resilientMCPTool{inner: inner, logger: zap.NewNop()}

	args := `{"title":"Test","parent_id":"abc123"}`
	_, err := wrapped.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(inner.lastArgs), &parsed); err != nil {
		t.Fatalf("failed to parse forwarded args: %v", err)
	}

	tags, ok := parsed["tags"]
	if !ok {
		t.Fatal("expected 'tags' to be injected")
	}
	arr, ok := tags.([]any)
	if !ok {
		t.Fatalf("expected 'tags' to be array, got %T", tags)
	}
	if len(arr) != 0 {
		t.Errorf("expected empty array for tags, got %v", arr)
	}
}

func TestNormalizeArgs_DoesNotOverwriteExistingField(t *testing.T) {
	inner := &stubMCPTool{
		info:      createNoteToolInfo(),
		returnVal: `{"ok":true}`,
	}
	wrapped := &resilientMCPTool{inner: inner, logger: zap.NewNop()}

	args := `{"title":"Test","parent_id":"abc","tags":["tag1","tag2"]}`
	_, err := wrapped.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(inner.lastArgs), &parsed); err != nil {
		t.Fatalf("failed to parse forwarded args: %v", err)
	}

	arr := parsed["tags"].([]any)
	if len(arr) != 2 {
		t.Errorf("expected 2 tags, got %d", len(arr))
	}
}

func TestNormalizeArgs_NoSchemaPassesThrough(t *testing.T) {
	inner := &stubMCPTool{
		info:      &schema.ToolInfo{Name: "simple_tool"},
		returnVal: `{"ok":true}`,
	}
	wrapped := &resilientMCPTool{inner: inner, logger: zap.NewNop()}

	args := `{"foo":"bar"}`
	_, err := wrapped.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.lastArgs != args {
		t.Errorf("expected args to pass through unchanged, got %q", inner.lastArgs)
	}
}

func TestErrorSoftening_ConvertsErrorToResult(t *testing.T) {
	inner := &stubMCPTool{
		info:      createNoteToolInfo(),
		returnErr: fmt.Errorf("failed to call mcp tool: invalid params: missing properties"),
	}
	wrapped := &resilientMCPTool{inner: inner, logger: zap.NewNop()}

	args := `{"title":"Test","parent_id":"abc","tags":[]}`
	result, err := wrapped.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("expected error to be softened, got Go error: %v", err)
	}
	if !strings.Contains(result, `"error"`) {
		t.Errorf("expected error in result, got: %s", result)
	}
	if !strings.Contains(result, "missing properties") {
		t.Errorf("expected original error message in result, got: %s", result)
	}
	if !strings.Contains(result, "hint") {
		t.Errorf("expected hint in result, got: %s", result)
	}
}

func TestWrapTools_PreservesNonInvokable(t *testing.T) {
	type baseOnly struct {
		tool.BaseTool
	}
	b := &baseOnly{}
	wrapped := WrapTools([]tool.BaseTool{b}, zap.NewNop())
	if len(wrapped) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(wrapped))
	}
	if _, ok := wrapped[0].(*resilientMCPTool); ok {
		t.Error("non-invokable tool should not be wrapped")
	}
}

func TestNormalizeArgs_StripsUnknownParameters(t *testing.T) {
	inner := &stubMCPTool{
		info:      createNoteToolInfo(),
		returnVal: `{"ok":true}`,
	}
	wrapped := &resilientMCPTool{inner: inner, logger: zap.NewNop()}

	// Model hallucinates "detailed" and "max_results" which are not in the schema.
	args := `{"title":"Test","parent_id":"abc","tags":["t1"],"detailed":false,"max_results":25}`
	_, err := wrapped.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(inner.lastArgs), &parsed); err != nil {
		t.Fatalf("failed to parse forwarded args: %v", err)
	}

	if _, exists := parsed["detailed"]; exists {
		t.Error("'detailed' should have been stripped (not in schema)")
	}
	if _, exists := parsed["max_results"]; exists {
		t.Error("'max_results' should have been stripped (not in schema)")
	}
	// Valid fields should still be present.
	if _, exists := parsed["title"]; !exists {
		t.Error("'title' should still be present")
	}
	if _, exists := parsed["tags"]; !exists {
		t.Error("'tags' should still be present")
	}
}

func TestSanitizeToolCallJSON_EscapedQuotes(t *testing.T) {
	// GPT-OSS produces: "start_time\":\"2026-03-20T14:00:00-07:00\",\"end_time\":\"..."
	input := `{"action":"create","user_google_email":"jesuscarrillo8@gmail.com","summary":"Let's go to Seattle","start_time\":\"2026-03-20T14:00:00-07:00\",\"end_time\":\"2026-03-20T22:00:00-07:00\",\"attendees\":[\"herrera8607@gmail.com\"]}`
	got := sanitizeToolCallJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("sanitized JSON should be valid, got parse error: %v\nJSON: %s", err, got)
	}

	if parsed["start_time"] != "2026-03-20T14:00:00-07:00" {
		t.Errorf("start_time = %v, want 2026-03-20T14:00:00-07:00", parsed["start_time"])
	}
	if parsed["end_time"] != "2026-03-20T22:00:00-07:00" {
		t.Errorf("end_time = %v, want 2026-03-20T22:00:00-07:00", parsed["end_time"])
	}
}

func TestSanitizeToolCallJSON_TrailingSpecialTokens(t *testing.T) {
	input := `{"action":"create","summary":"test"}<|end|><|start|>assistant<|channel|>analysis<|message|>some reasoning`
	got := sanitizeToolCallJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("sanitized JSON should be valid, got parse error: %v\nJSON: %s", err, got)
	}
	if parsed["action"] != "create" {
		t.Errorf("action = %v, want create", parsed["action"])
	}
}

func TestSanitizeToolCallJSON_BothEscapedAndTokens(t *testing.T) {
	input := `{"action":"create","summary":"Let's go","start_time\":\"2026-03-20T14:00:00-07:00\",\"end_time\":\"2026-03-20T22:00:00-07:00\"}<|end|><|start|>assistant<|channel|>analysis<|message|>oops`
	got := sanitizeToolCallJSON(input)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("sanitized JSON should be valid, got parse error: %v\nJSON: %s", err, got)
	}
	if parsed["start_time"] != "2026-03-20T14:00:00-07:00" {
		t.Errorf("start_time = %v", parsed["start_time"])
	}
}

func TestSanitizeToolCallJSON_ValidJSONUntouched(t *testing.T) {
	input := `{"action":"create","start_time":"2026-03-20T14:00:00-07:00"}`
	got := sanitizeToolCallJSON(input)
	if got != input {
		t.Errorf("valid JSON should not be modified, got %s", got)
	}
}

func TestSanitizeToolCallJSON_EmptyString(t *testing.T) {
	if got := sanitizeToolCallJSON(""); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

func TestNormalizeArgs_DoesNotInjectStringDefaults(t *testing.T) {
	inner := &stubMCPTool{
		info:      createNoteToolInfo(),
		returnVal: `{"ok":true}`,
	}
	wrapped := &resilientMCPTool{inner: inner, logger: zap.NewNop()}

	args := `{"title":"Test","tags":["t1"]}`
	_, err := wrapped.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(inner.lastArgs), &parsed); err != nil {
		t.Fatalf("failed to parse forwarded args: %v", err)
	}

	if _, exists := parsed["parent_id"]; exists {
		t.Error("parent_id should NOT be auto-injected (string fields need explicit values)")
	}
}
