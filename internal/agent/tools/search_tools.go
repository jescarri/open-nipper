package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

// SearchToolsInput is the parameter for the search_tools tool.
type SearchToolsInput struct {
	Intent string `json:"intent" jsonschema:"description=Short description of what you want to do (e.g. 'create a calendar event' or 'turn off the lights')"`
}

// SearchToolsResultEntry is a single matched tool.
type SearchToolsResultEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SearchToolsResult is the output of search_tools.
type SearchToolsResult struct {
	Tools []SearchToolsResultEntry `json:"tools"`
	Count int                      `json:"count"`
}

// SearchToolsExecutor holds the catalog and matcher for the search_tools tool.
type SearchToolsExecutor struct {
	catalog []ToolCatalogEntry
	matcher ToolMatcher
}

// NewSearchToolsExecutor creates a search_tools executor with the given catalog and matcher.
func NewSearchToolsExecutor(catalog []ToolCatalogEntry, matcher ToolMatcher) *SearchToolsExecutor {
	return &SearchToolsExecutor{
		catalog: catalog,
		matcher: matcher,
	}
}

// ExecSearchTools matches tools against the intent and returns the results.
func (e *SearchToolsExecutor) ExecSearchTools(ctx context.Context, input *SearchToolsInput) (string, error) {
	if input == nil || input.Intent == "" {
		return `{"tools":[],"count":0}`, nil
	}

	const maxResults = 10
	matched, err := e.matcher.Match(ctx, input.Intent, e.catalog, maxResults)
	if err != nil {
		return "", fmt.Errorf("matching tools: %w", err)
	}

	// Build result with descriptions from catalog.
	descMap := make(map[string]string, len(e.catalog))
	for _, entry := range e.catalog {
		descMap[entry.Name] = entry.Description
	}

	result := SearchToolsResult{
		Tools: make([]SearchToolsResultEntry, 0, len(matched)),
		Count: len(matched),
	}
	for _, name := range matched {
		result.Tools = append(result.Tools, SearchToolsResultEntry{
			Name:        name,
			Description: descMap[name],
		})
	}

	b, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshaling search result: %w", err)
	}
	return string(b), nil
}

// BuildSearchToolsTool creates the search_tools eino BaseTool.
func BuildSearchToolsTool(catalog []ToolCatalogEntry, matcher ToolMatcher) (tool.BaseTool, error) {
	executor := NewSearchToolsExecutor(catalog, matcher)
	return toolutils.InferTool(
		"search_tools",
		"Search for available tools by describing what you want to do. "+
			"Call this BEFORE using any MCP tool. "+
			"Pass a short intent description (e.g. 'create a calendar event', 'turn off lights'). "+
			"Returns matching tool names and descriptions that will be activated for your use.",
		executor.ExecSearchTools,
	)
}
