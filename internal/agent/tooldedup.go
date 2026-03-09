package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const maxIdenticalToolCalls = 4

// maxIdenticalSkillExecCalls is the limit for skill_exec; lower so the model
// stops re-calling the same skill and uses the first result to create the note.
const maxIdenticalSkillExecCalls = 2

type dedupKey struct {
	name string
	hash [sha256.Size]byte
}

// toolCallTracker counts how many times the same tool+args combination
// has been invoked within a single agent Generate run.
type toolCallTracker struct {
	mu    sync.Mutex
	calls map[dedupKey]int
}

func newToolCallTracker() *toolCallTracker {
	return &toolCallTracker{calls: make(map[dedupKey]int)}
}

// check increments the call counter and returns (count, exceeded).
func (t *toolCallTracker) check(name, argsJSON string) (int, bool) {
	key := dedupKey{name: name, hash: sha256.Sum256([]byte(argsJSON))}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls[key]++
	count := t.calls[key]
	limit := maxIdenticalToolCalls
	if name == "skill_exec" {
		limit = maxIdenticalSkillExecCalls
	}
	return count, count > limit
}

// dedupTool wraps an InvokableTool and short-circuits repeated identical calls.
type dedupTool struct {
	inner   tool.InvokableTool
	tracker *toolCallTracker
}

func (d *dedupTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return d.inner.Info(ctx)
}

func (d *dedupTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	info, _ := d.inner.Info(ctx)
	name := ""
	if info != nil {
		name = info.Name
	}
	count, exceeded := d.tracker.check(name, argumentsInJSON)
	if exceeded {
		msg := fmt.Sprintf(
			`{"error":"Tool %q has been called %d times with identical arguments. The result will not change — do NOT call it again. Use the data you already have and proceed to the next step."}`,
			name, count,
		)
		if name == "web_fetch" {
			msg = `{"error":"web_fetch has been called too many times with the same URL. The result will not change — do NOT retry. Use the title from the previous result and your knowledge to produce a summary. Proceed to create the Joplin note."}`
		}
		if name == "skill_exec" {
			msg = `{"error":"skill_exec has been called too many times with the same arguments. Do NOT call skill_exec again. Use the captions from the first tool result to write a summary, then call list_folders to get the youtube-videos folder ID (or create_folder if missing), then call create_note with that parent_id and tag_names. You MUST call create_note."}`
		}
		return msg, nil
	}
	return d.inner.InvokableRun(ctx, argumentsInJSON, opts...)
}

// wrapToolsWithDedup wraps every InvokableTool in a dedup guard that shares
// a single call tracker. Non-invokable tools are passed through unchanged.
func wrapToolsWithDedup(tools []tool.BaseTool) []tool.BaseTool {
	tracker := newToolCallTracker()
	wrapped := make([]tool.BaseTool, len(tools))
	for i, t := range tools {
		if inv, ok := t.(tool.InvokableTool); ok {
			wrapped[i] = &dedupTool{inner: inv, tracker: tracker}
		} else {
			wrapped[i] = t
		}
	}
	return wrapped
}

// descSanitizedTool wraps an InvokableTool to guarantee the ToolInfo.Desc
// field is never empty. vLLM's openai_harmony parser (used by gpt-oss models)
// rejects tool definitions where description is null/omitted, which happens
// when Desc is "" due to the omitempty JSON tag in the eino-ext ACL layer.
type descSanitizedTool struct {
	inner tool.InvokableTool
}

func (d *descSanitizedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	info, err := d.inner.Info(ctx)
	if err != nil {
		return info, err
	}
	if info != nil && info.Desc == "" {
		info.Desc = info.Name
	}
	return info, nil
}

func (d *descSanitizedTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return d.inner.InvokableRun(ctx, argumentsInJSON, opts...)
}

// sanitizeToolDescriptions ensures every tool has a non-empty description so
// that OpenAI-compatible endpoints (vLLM, etc.) don't reject the request.
func sanitizeToolDescriptions(tools []tool.BaseTool) []tool.BaseTool {
	out := make([]tool.BaseTool, len(tools))
	for i, t := range tools {
		if inv, ok := t.(tool.InvokableTool); ok {
			out[i] = &descSanitizedTool{inner: inv}
		} else {
			out[i] = t
		}
	}
	return out
}
