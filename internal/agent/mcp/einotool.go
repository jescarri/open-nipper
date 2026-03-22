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
		// Normalize JSON Schema boolean schemas (true/false) to object form so
		// downstream (e.g. OpenAI API) does not reject with "Unrecognized schema: true".
		inputSchemaBytes, err = normalizeBooleanSchemas(inputSchemaBytes)
		if err != nil {
			return nil, fmt.Errorf("normalizing input schema for tool %q: %w", t.Name, err)
		}
		parsedSchema := &jsonschema.Schema{}
		if err := json.Unmarshal(inputSchemaBytes, parsedSchema); err != nil {
			return nil, fmt.Errorf("parsing input schema for tool %q: %w", t.Name, err)
		}
		fixPatterns(parsedSchema)
		parsedSchema = fixBooleanSchemaNodes(parsedSchema)

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

// normalizeBooleanSchemas rewrites raw JSON schema bytes so that any boolean
// schema (true or false) is replaced by an object schema. Some MCP servers (e.g.
// Google Workspace MCP) emit "properties": {"foo": true}, which causes APIs
// like OpenAI to return "Unrecognized schema: true". We replace true with
// {"type": "object"} and false with {"type": "object", "description": "unsupported"}.
func normalizeBooleanSchemas(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	normalized := normalizeBooleanSchemasRecurse(v)
	return json.Marshal(normalized)
}

func normalizeBooleanSchemasRecurse(v any) any {
	switch x := v.(type) {
	case bool:
		if x {
			return map[string]any{"type": "object"}
		}
		return map[string]any{"type": "object", "description": "unsupported"}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeBooleanSchemasRecurse(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeBooleanSchemasRecurse(val)
		}
		return out
	default:
		return v
	}
}

// fixBooleanSchemaNodes walks the typed jsonschema.Schema tree and replaces any
// sub-schema whose MarshalJSON output is bare "true" or "false" with a concrete
// object schema. The eino-contrib/jsonschema package stores JSON Schema boolean
// schemas (section 4.3.2) in a private boolean field; its MarshalJSON then emits
// the raw boolean literal, which llama.cpp and other strict parsers reject with
// "Unrecognized schema: true". This pass catches cases the raw-JSON normalization
// (normalizeBooleanSchemas) cannot prevent — e.g. when UnmarshalJSON internally
// creates boolean schema nodes.
func fixBooleanSchemaNodes(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}
	// Quick check: a boolean schema has no exported fields populated.
	if s.Type == "" && s.Properties == nil && s.Items == nil &&
		s.AdditionalProperties == nil && len(s.AllOf) == 0 &&
		len(s.AnyOf) == 0 && len(s.OneOf) == 0 &&
		s.Description == "" && len(s.Enum) == 0 && s.Ref == "" &&
		s.Not == nil && s.If == nil {
		b, err := json.Marshal(s)
		if err == nil {
			switch string(b) {
			case "true":
				return &jsonschema.Schema{Type: "object"}
			case "false":
				r := &jsonschema.Schema{Type: "object"}
				r.Description = "unsupported"
				return r
			}
		}
	}

	// Recurse into all sub-schema positions.
	s.AdditionalProperties = fixBooleanSchemaNodes(s.AdditionalProperties)
	s.Items = fixBooleanSchemaNodes(s.Items)
	s.Not = fixBooleanSchemaNodes(s.Not)
	s.If = fixBooleanSchemaNodes(s.If)
	s.Then = fixBooleanSchemaNodes(s.Then)
	s.Else = fixBooleanSchemaNodes(s.Else)

	if s.Properties != nil {
		for pair := s.Properties.Oldest(); pair != nil; pair = pair.Next() {
			s.Properties.Set(pair.Key, fixBooleanSchemaNodes(pair.Value))
		}
	}
	for i := range s.AllOf {
		s.AllOf[i] = fixBooleanSchemaNodes(s.AllOf[i])
	}
	for i := range s.AnyOf {
		s.AnyOf[i] = fixBooleanSchemaNodes(s.AnyOf[i])
	}
	for i := range s.OneOf {
		s.OneOf[i] = fixBooleanSchemaNodes(s.OneOf[i])
	}
	for i := range s.PrefixItems {
		s.PrefixItems[i] = fixBooleanSchemaNodes(s.PrefixItems[i])
	}
	for k, v := range s.PatternProperties {
		s.PatternProperties[k] = fixBooleanSchemaNodes(v)
	}
	for k, v := range s.DependentSchemas {
		s.DependentSchemas[k] = fixBooleanSchemaNodes(v)
	}
	for k, v := range s.Definitions {
		s.Definitions[k] = fixBooleanSchemaNodes(v)
	}
	return s
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
