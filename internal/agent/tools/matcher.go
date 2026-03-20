package tools

import (
	"context"
	"strings"
)

// ToolCatalogEntry holds metadata for a single tool in the catalog.
// Used by ToolMatcher implementations to score relevance.
type ToolCatalogEntry struct {
	Name        string   // tool name (e.g. "manage_event")
	Description string   // short description
	ServerName  string   // originating MCP server (e.g. "google-workspace")
	Tags        []string // optional semantic tags (e.g. "calendar", "email")
}

// ToolMatcher scores tools against a user intent string.
// Implementations can use keyword matching, embeddings, or hybrid approaches.
type ToolMatcher interface {
	// Match returns tool names ranked by relevance to the intent.
	// maxResults caps the returned set (0 = no limit).
	Match(ctx context.Context, intent string, catalog []ToolCatalogEntry, maxResults int) ([]string, error)
}

// KeywordToolMatcher matches tools by checking if the intent string contains
// substrings from tool names or descriptions. Fast, no external dependencies.
type KeywordToolMatcher struct{}

// Match returns tool names whose name or description words overlap with the intent.
func (m *KeywordToolMatcher) Match(_ context.Context, intent string, catalog []ToolCatalogEntry, maxResults int) ([]string, error) {
	if intent == "" {
		return nil, nil
	}
	lower := strings.ToLower(intent)

	type scored struct {
		name  string
		score int
	}
	var matches []scored

	for _, entry := range catalog {
		score := 0

		// Check tool name parts (split on _ and camelCase boundaries).
		nameLower := strings.ToLower(entry.Name)
		for _, part := range splitToolName(nameLower) {
			if len(part) >= 3 && strings.Contains(lower, part) {
				score += 3
			}
		}

		// Check description words.
		descLower := strings.ToLower(entry.Description)
		for _, word := range strings.Fields(descLower) {
			word = strings.Trim(word, ".,;:()[]\"'")
			if len(word) >= 4 && strings.Contains(lower, word) {
				score++
			}
		}

		// Check semantic tags.
		for _, tag := range entry.Tags {
			if strings.Contains(lower, strings.ToLower(tag)) {
				score += 2
			}
		}

		if score > 0 {
			matches = append(matches, scored{name: entry.Name, score: score})
		}
	}

	// Sort by score descending (simple insertion sort — catalog is small).
	for i := 1; i < len(matches); i++ {
		for j := i; j > 0 && matches[j].score > matches[j-1].score; j-- {
			matches[j], matches[j-1] = matches[j-1], matches[j]
		}
	}

	if maxResults > 0 && len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.name
	}
	return names, nil
}

// splitToolName splits a tool name on underscores and common separators.
func splitToolName(name string) []string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})
	return parts
}
