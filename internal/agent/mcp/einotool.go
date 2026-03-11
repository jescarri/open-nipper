// Package mcp provides an adapter that exposes tools from an official MCP SDK
// ClientSession as eino BaseTool/InvokableTool instances.
//
// This replaces the cloudwego/eino-ext/components/tool/mcp adapter which
// depends on the unofficial mark3labs/mcp-go library.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// GetToolsFromSession lists tools from a connected MCP ClientSession and wraps
// each one as an eino InvokableTool. The softError flag controls whether
// server-side tool errors (IsError: true) are returned as regular string
// results (true) or as Go errors (false).
func GetToolsFromSession(ctx context.Context, session *mcpsdk.ClientSession, softError bool) ([]tool.BaseTool, error) {
	result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("listing MCP tools: %w", err)
	}

	tools := make([]tool.BaseTool, 0, len(result.Tools))
	for _, t := range result.Tools {
		inputSchemaBytes, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshaling input schema for tool %q: %w", t.Name, err)
		}
		parsedSchema := &jsonschema.Schema{}
		if err := json.Unmarshal(inputSchemaBytes, parsedSchema); err != nil {
			return nil, fmt.Errorf("parsing input schema for tool %q: %w", t.Name, err)
		}
		fixPatterns(parsedSchema)

		tools = append(tools, &mcpTool{
			session:   session,
			name:      t.Name,
			softError: softError,
			info: &schema.ToolInfo{
				Name:        t.Name,
				Desc:        t.Description,
				ParamsOneOf: schema.NewParamsOneOfByJSONSchema(parsedSchema),
			},
		})
	}
	return tools, nil
}

// mcpTool wraps a single MCP tool as an eino InvokableTool.
type mcpTool struct {
	session   *mcpsdk.ClientSession
	name      string
	softError bool
	info      *schema.ToolInfo
}

func (t *mcpTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *mcpTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("invalid tool arguments JSON: %w", err)
	}

	result, err := t.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("calling MCP tool %q: %w", t.name, err)
	}

	if t.softError {
		result.IsError = false
	}

	resultBytes, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshaling MCP tool result: %w", err)
	}

	if result.IsError {
		return "", fmt.Errorf("MCP tool %q returned error: %s", t.name, string(resultBytes))
	}

	return string(resultBytes), nil
}

// fixPatterns recursively walks a JSON schema and ensures every "pattern"
// value starts with '^' and ends with '$', as required by the OpenAI API.
func fixPatterns(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if s.Pattern != "" {
		if !strings.HasPrefix(s.Pattern, "^") {
			s.Pattern = "^" + s.Pattern
		}
		if !strings.HasSuffix(s.Pattern, "$") {
			s.Pattern = s.Pattern + "$"
		}
	}
	if s.Properties != nil {
		for pair := s.Properties.Oldest(); pair != nil; pair = pair.Next() {
			fixPatterns(pair.Value)
		}
	}
	if s.Items != nil {
		fixPatterns(s.Items)
	}
	if s.AdditionalProperties != nil {
		fixPatterns(s.AdditionalProperties)
	}
	for _, sub := range s.AllOf {
		fixPatterns(sub)
	}
	for _, sub := range s.AnyOf {
		fixPatterns(sub)
	}
	for _, sub := range s.OneOf {
		fixPatterns(sub)
	}
	fixPatterns(s.Not)
	fixPatterns(s.If)
	fixPatterns(s.Then)
	fixPatterns(s.Else)
	for _, sub := range s.PrefixItems {
		fixPatterns(sub)
	}
	for _, sub := range s.PatternProperties {
		fixPatterns(sub)
	}
	for _, sub := range s.DependentSchemas {
		fixPatterns(sub)
	}
	for _, sub := range s.Definitions {
		fixPatterns(sub)
	}
}
