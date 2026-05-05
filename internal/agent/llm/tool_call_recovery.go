package llm

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// hermesToolCallRe matches <tool_call>BODY</tool_call> and the alias
// <function_call>BODY</function_call>. Some local models (Qwen3, Hermes,
// OpenChat) emit tool calls as text inside these tags instead of populating
// the structured tool_calls field.
var hermesToolCallRe = regexp.MustCompile(`(?s)<(tool_call|function_call)\s*>(.*?)</(?:tool_call|function_call)\s*>`)

// qwenFunctionRe matches the Qwen XML pseudo-call <function=NAME>...</function>.
var qwenFunctionRe = regexp.MustCompile(`(?s)<function=([^\s>]+)\s*>(.*?)</function\s*>`)

// qwenParamRe matches <parameter=NAME>VALUE</parameter> within a Qwen function block.
var qwenParamRe = regexp.MustCompile(`(?s)<parameter=([^\s>]+)\s*>(.*?)</parameter\s*>`)

// recoverEmbeddedToolCalls scans msg.Content and msg.ReasoningContent for
// Hermes-style <tool_call>{...}</tool_call> blocks (and the Qwen XML
// pseudo-call form) that some models emit as text instead of as structured
// tool_calls. Recovered calls are appended to msg.ToolCalls and the matching
// blocks are stripped from Content / ReasoningContent. Returns the count of
// recovered calls.
func recoverEmbeddedToolCalls(msg *schema.Message) int {
	if msg == nil {
		return 0
	}
	count := 0
	if cleaned, calls := extractEmbeddedToolCalls(msg.Content); len(calls) > 0 {
		msg.Content = cleaned
		msg.ToolCalls = append(msg.ToolCalls, calls...)
		count += len(calls)
	}
	if cleaned, calls := extractEmbeddedToolCalls(msg.ReasoningContent); len(calls) > 0 {
		msg.ReasoningContent = cleaned
		msg.ToolCalls = append(msg.ToolCalls, calls...)
		count += len(calls)
	}
	return count
}

// extractEmbeddedToolCalls returns the input string with successfully-parsed
// tool-call blocks removed, plus the synthesized ToolCalls. Blocks that fail
// to parse are left in place so debugging output is preserved.
func extractEmbeddedToolCalls(s string) (string, []schema.ToolCall) {
	if s == "" {
		return s, nil
	}
	if !strings.Contains(s, "<tool_call") && !strings.Contains(s, "<function_call") {
		return s, nil
	}

	matches := hermesToolCallRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}

	var calls []schema.ToolCall
	var sb strings.Builder
	prev := 0
	for _, m := range matches {
		// m[0]:m[1] = whole match; m[4]:m[5] = body capture group.
		sb.WriteString(s[prev:m[0]])
		body := s[m[4]:m[5]]
		if call, ok := parseToolCallBody(body); ok {
			calls = append(calls, call)
		} else {
			// Couldn't parse — preserve the original block so it remains
			// visible for debugging.
			sb.WriteString(s[m[0]:m[1]])
		}
		prev = m[1]
	}
	sb.WriteString(s[prev:])

	if len(calls) == 0 {
		return s, nil
	}
	return strings.TrimSpace(sb.String()), calls
}

func parseToolCallBody(body string) (schema.ToolCall, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return schema.ToolCall{}, false
	}

	// Form 1: JSON object — {"name": "...", "arguments": ...}.
	if strings.HasPrefix(body, "{") {
		if call, ok := parseJSONToolCall(body); ok {
			return call, true
		}
	}

	// Form 2: Qwen XML — <function=NAME><parameter=KEY>VALUE</parameter>...</function>.
	if call, ok := parseQwenXMLToolCall(body); ok {
		return call, true
	}

	return schema.ToolCall{}, false
}

func parseJSONToolCall(body string) (schema.ToolCall, bool) {
	type rawCall struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	var raw rawCall
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return schema.ToolCall{}, false
	}
	if strings.TrimSpace(raw.Name) == "" {
		return schema.ToolCall{}, false
	}

	args := strings.TrimSpace(string(raw.Arguments))
	switch {
	case args == "", args == "null":
		args = "{}"
	case strings.HasPrefix(args, `"`):
		// String form: "{\"key\": \"val\"}" — unescape once.
		var s string
		if err := json.Unmarshal([]byte(args), &s); err == nil {
			args = s
		}
	}

	return schema.ToolCall{
		ID:   newRecoveredToolCallID(),
		Type: "function",
		Function: schema.FunctionCall{
			Name:      strings.TrimSpace(raw.Name),
			Arguments: args,
		},
	}, true
}

func parseQwenXMLToolCall(body string) (schema.ToolCall, bool) {
	fnMatch := qwenFunctionRe.FindStringSubmatch(body)
	if len(fnMatch) != 3 {
		return schema.ToolCall{}, false
	}
	name := strings.TrimSpace(fnMatch[1])
	if name == "" {
		return schema.ToolCall{}, false
	}
	params := map[string]string{}
	for _, pm := range qwenParamRe.FindAllStringSubmatch(fnMatch[2], -1) {
		if len(pm) != 3 {
			continue
		}
		params[strings.TrimSpace(pm[1])] = strings.TrimSpace(pm[2])
	}
	args := "{}"
	if len(params) > 0 {
		if b, err := json.Marshal(params); err == nil {
			args = string(b)
		}
	}
	return schema.ToolCall{
		ID:   newRecoveredToolCallID(),
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}, true
}

// newRecoveredToolCallID returns a unique synthetic ID for a recovered call.
// The "recovered_" prefix makes these calls easy to spot in logs.
func newRecoveredToolCallID() string {
	return "recovered_" + uuid.NewString()
}
