package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestExecSearchTools_MatchesTools(t *testing.T) {
	catalog := []ToolCatalogEntry{
		{Name: "manage_event", Description: "Create, update, or delete a calendar event"},
		{Name: "get_events", Description: "Retrieve events from a Google Calendar"},
		{Name: "HassTurnOn", Description: "Turn on a device"},
	}
	executor := NewSearchToolsExecutor(catalog, &KeywordToolMatcher{})

	result, err := executor.ExecSearchTools(context.Background(), &SearchToolsInput{
		Intent: "create a calendar event",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed SearchToolsResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if parsed.Count == 0 {
		t.Fatal("expected at least one match")
	}

	found := false
	for _, tool := range parsed.Tools {
		if tool.Name == "manage_event" {
			found = true
			if tool.Description == "" {
				t.Error("expected description in result")
			}
		}
	}
	if !found {
		t.Errorf("expected manage_event in results, got %+v", parsed.Tools)
	}
}

func TestExecSearchTools_EmptyIntent(t *testing.T) {
	executor := NewSearchToolsExecutor(nil, &KeywordToolMatcher{})

	result, err := executor.ExecSearchTools(context.Background(), &SearchToolsInput{Intent: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed SearchToolsResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if parsed.Count != 0 {
		t.Errorf("expected 0 results for empty intent, got %d", parsed.Count)
	}
}

func TestExecSearchTools_NilInput(t *testing.T) {
	executor := NewSearchToolsExecutor(nil, &KeywordToolMatcher{})

	result, err := executor.ExecSearchTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != `{"tools":[],"count":0}` {
		t.Errorf("unexpected result for nil input: %s", result)
	}
}

func TestBuildSearchToolsTool_Builds(t *testing.T) {
	catalog := []ToolCatalogEntry{
		{Name: "test_tool", Description: "a test tool"},
	}
	tool, err := BuildSearchToolsTool(catalog, &KeywordToolMatcher{})
	if err != nil {
		t.Fatalf("failed to build search_tools: %v", err)
	}

	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("failed to get tool info: %v", err)
	}
	if info.Name != "search_tools" {
		t.Errorf("expected name 'search_tools', got %q", info.Name)
	}
}
