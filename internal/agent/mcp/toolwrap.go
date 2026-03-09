package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

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

// normalizeArgs injects zero-value defaults for required schema fields that
// are missing from the model's arguments. Currently handles array → [].
func (r *resilientMCPTool) normalizeArgs(info *schema.ToolInfo, argsJSON string) string {
	if info == nil || info.ParamsOneOf == nil {
		return argsJSON
	}

	jsSchema, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil || jsSchema == nil || len(jsSchema.Required) == 0 {
		return argsJSON
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}

	changed := false
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
