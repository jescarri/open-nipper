package tools

import (
	"context"
	"testing"
)

func TestKeywordToolMatcher_BasicMatch(t *testing.T) {
	catalog := []ToolCatalogEntry{
		{Name: "manage_event", Description: "Create, update, or delete a calendar event"},
		{Name: "get_events", Description: "Retrieve events from a Google Calendar"},
		{Name: "HassTurnOn", Description: "Turn on a device or entity"},
		{Name: "HassTurnOff", Description: "Turn off a device or entity"},
		{Name: "create_note", Description: "Create a Joplin note"},
		{Name: "send_gmail_message", Description: "Send an email via Gmail"},
	}
	matcher := &KeywordToolMatcher{}

	tests := []struct {
		name       string
		intent     string
		wantAny    []string // at least one of these should be in results
		wantNoneOf []string // none of these should be in results
	}{
		{
			name:       "calendar event",
			intent:     "create a calendar event for tomorrow",
			wantAny:    []string{"manage_event", "get_events"},
			wantNoneOf: []string{"HassTurnOn"},
		},
		{
			name:       "turn off lights",
			intent:     "turn off the office light",
			wantAny:    []string{"HassTurnOff", "HassTurnOn"},
			wantNoneOf: []string{"manage_event", "send_gmail_message"},
		},
		{
			name:       "send email",
			intent:     "send an email to john@example.com",
			wantAny:    []string{"send_gmail_message"},
			wantNoneOf: []string{"HassTurnOn"},
		},
		{
			name:    "create a note",
			intent:  "create a joplin note about the meeting",
			wantAny: []string{"create_note"},
		},
		{
			name:   "empty intent",
			intent: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := matcher.Match(context.Background(), tt.intent, catalog, 0)
			if err != nil {
				t.Fatalf("Match error: %v", err)
			}

			gotSet := make(map[string]bool, len(got))
			for _, name := range got {
				gotSet[name] = true
			}

			for _, want := range tt.wantAny {
				if !gotSet[want] {
					t.Errorf("expected %q in results, got %v", want, got)
				}
			}
			for _, noWant := range tt.wantNoneOf {
				if gotSet[noWant] {
					t.Errorf("did not expect %q in results, got %v", noWant, got)
				}
			}
		})
	}
}

func TestKeywordToolMatcher_MaxResults(t *testing.T) {
	catalog := []ToolCatalogEntry{
		{Name: "tool1", Description: "does things with events"},
		{Name: "tool2", Description: "manages events"},
		{Name: "tool3", Description: "creates events"},
		{Name: "tool4", Description: "deletes events"},
		{Name: "tool5", Description: "lists events"},
	}
	matcher := &KeywordToolMatcher{}

	got, err := matcher.Match(context.Background(), "events", catalog, 3)
	if err != nil {
		t.Fatalf("Match error: %v", err)
	}
	if len(got) > 3 {
		t.Errorf("expected at most 3 results, got %d: %v", len(got), got)
	}
}

func TestKeywordToolMatcher_Tags(t *testing.T) {
	catalog := []ToolCatalogEntry{
		{Name: "manage_event", Description: "manage events", Tags: []string{"calendar", "scheduling"}},
		{Name: "HassTurnOn", Description: "turn on device", Tags: []string{"iot", "home"}},
	}
	matcher := &KeywordToolMatcher{}

	got, err := matcher.Match(context.Background(), "I need something for scheduling", catalog, 0)
	if err != nil {
		t.Fatalf("Match error: %v", err)
	}

	found := false
	for _, name := range got {
		if name == "manage_event" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected manage_event to match via 'scheduling' tag, got %v", got)
	}
}
