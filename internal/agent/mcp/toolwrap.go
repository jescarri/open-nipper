package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// specialTokenRe matches chat-template special tokens leaked by local models
// (e.g. <|end|>, <|start|>, <|channel|>, <|message|>, <|constrain|>).
var specialTokenRe = regexp.MustCompile(`<\|[^|]*\|>`)

// sanitizeToolCallJSON fixes common JSON corruption from open-source models:
//  1. Strips trailing chat-template tokens appended after the JSON closing brace
//     (e.g. }<|end|><|start|>assistant<|channel|>analysis<|message|>...).
//  2. Fixes escaped quotes inside JSON keys/values that some models produce
//     (e.g. "start_time\":\"2026-03-20T14:00:00\" → "start_time":"2026-03-20T14:00:00").
//
// Returns the cleaned JSON string for parsing.
func sanitizeToolCallJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}

	// 1. Truncate at first special token marker outside a JSON string context.
	// Find the last '}' or ']' to identify where the JSON object/array ends,
	// then discard everything after it.
	lastBrace := strings.LastIndexAny(s, "}]")
	if lastBrace >= 0 && lastBrace < len(s)-1 {
		tail := s[lastBrace+1:]
		if specialTokenRe.MatchString(tail) {
			s = s[:lastBrace+1]
		}
	}

	// 2. Fix escaped quotes pattern: \" inside the raw JSON that shouldn't be escaped.
	// GPT-OSS models produce keys like: "start_time\": \"2026-03-20T14:00:00-07:00\"
	// which after JSON parsing creates a single key containing the value.
	// Try parsing first; only apply the fix if parsing fails.
	var probe map[string]any
	if json.Unmarshal([]byte(s), &probe) == nil {
		return s // valid JSON, no fix needed
	}

	// Replace \" with " and re-validate. The pattern is typically:
	//   "key\": \"value\"  →  "key": "value"
	// We only replace backslash-quote sequences that appear outside already-valid strings.
	fixed := strings.ReplaceAll(s, `\"`, `"`)
	// After replacement we may have doubled quotes ("") at key boundaries; fix those too.
	fixed = strings.ReplaceAll(fixed, `""`, `"`)

	if json.Unmarshal([]byte(fixed), &probe) == nil {
		return fixed
	}

	// If still invalid, return the original (normalizeArgs will handle the parse failure).
	return s
}

// WrapTools applies two resilience layers around each MCP InvokableTool:
//
//  1. Argument normalisation — injects zero-values for required array fields
//     that the model omitted (e.g. "tags": []).
//  2. Error softening — if InvokableRun returns a Go error (client-side
//     validation, transport, etc.) it is converted into a successful tool
//     result string so the ReAct loop can read the error and self-correct
//     instead of crashing with a NodeRunError.
func WrapTools(tools []tool.BaseTool, logger *zap.Logger) []tool.BaseTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	out := make([]tool.BaseTool, len(tools))
	for i, t := range tools {
		if inv, ok := t.(tool.InvokableTool); ok {
			out[i] = &resilientMCPTool{inner: inv, logger: logger}
		} else {
			out[i] = t
		}
	}
	return out
}

type resilientMCPTool struct {
	inner  tool.InvokableTool
	logger *zap.Logger
}

func (r *resilientMCPTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return r.inner.Info(ctx)
}

// maxToolResultBytes caps the size of a tool result to avoid blowing the
// context window. ~12 000 chars ≈ ~3 000 tokens — leaves plenty of room
// for system prompt, history, and the model's response.
const maxToolResultBytes = 12_000

func (r *resilientMCPTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	info, _ := r.inner.Info(ctx)
	name := ""
	if info != nil {
		name = info.Name
	}

	normalized := r.normalizeArgs(info, argumentsInJSON)

	result, err := r.inner.InvokableRun(ctx, normalized, opts...)
	if err != nil {
		r.logger.Warn("MCP tool invocation error softened",
			zap.String("tool", name),
			zap.Error(err),
		)
		return fmt.Sprintf(
			`{"error":%q,"hint":"Check the required parameters and try again. Use the exact field names from the tool schema."}`,
			err.Error(),
		), nil
	}
	if len(result) > maxToolResultBytes {
		r.logger.Warn("truncating oversized MCP tool result",
			zap.String("tool", name),
			zap.Int("originalBytes", len(result)),
			zap.Int("cappedBytes", maxToolResultBytes),
		)
		result = result[:maxToolResultBytes] + "\n...[truncated — result too large]"
	}
	return result, nil
}

// normalizeArgs fixes common model mistakes in tool call arguments:
//  1. Injects zero-values for required array fields the model omitted.
//  2. Strips parameters not defined in the schema (models like Llama often
//     hallucinate extra fields such as "detailed" or "max_results").
func (r *resilientMCPTool) normalizeArgs(info *schema.ToolInfo, argsJSON string) string {
	if info == nil || info.ParamsOneOf == nil {
		return argsJSON
	}

	jsSchema, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil || jsSchema == nil {
		return argsJSON
	}

	// Sanitize corrupted JSON from local models (escaped quotes, trailing special tokens).
	sanitized := sanitizeToolCallJSON(argsJSON)
	if sanitized != argsJSON {
		r.logger.Debug("sanitized tool call JSON",
			zap.Int("originalLen", len(argsJSON)),
			zap.Int("sanitizedLen", len(sanitized)),
		)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(sanitized), &args); err != nil {
		return argsJSON // return original on parse failure
	}

	changed := false

	// Strip unknown parameters not defined in the schema.
	if jsSchema.Properties != nil {
		for key := range args {
			if _, defined := jsSchema.Properties.Get(key); !defined {
				delete(args, key)
				changed = true
				r.logger.Debug("stripped unknown parameter from tool call",
					zap.String("field", key),
				)
			}
		}
	}

	// Inject empty arrays for missing required array fields.
	for _, reqField := range jsSchema.Required {
		if _, exists := args[reqField]; exists {
			continue
		}
		if jsSchema.Properties == nil {
			continue
		}
		propSchema, ok := jsSchema.Properties.Get(reqField)
		if !ok || propSchema == nil {
			continue
		}

		if strings.EqualFold(propSchema.Type, "array") {
			args[reqField] = []any{}
			changed = true
			r.logger.Debug("injected empty array for missing required field",
				zap.String("field", reqField),
			)
		}
	}

	if !changed {
		return argsJSON
	}
	out, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	return string(out)
}
