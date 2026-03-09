package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type fakeTool struct {
	name    string
	calls   int
	lastArg string
}

func (f *fakeTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name}, nil
}

func (f *fakeTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	f.calls++
	f.lastArg = argsJSON
	return `{"ok":true}`, nil
}

func TestDedupTool_AllowsDistinctCalls(t *testing.T) {
	inner := &fakeTool{name: "web_fetch"}
	tracker := newToolCallTracker()
	dt := &dedupTool{inner: inner, tracker: tracker}
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		args := `{"url":"https://example.com/` + string(rune('a'+i)) + `"}`
		res, err := dt.InvokableRun(ctx, args)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if strings.Contains(res, `"error"`) {
			t.Fatalf("call %d: should not be blocked (distinct args): %s", i, res)
		}
	}
	if inner.calls != 10 {
		t.Errorf("expected 10 inner calls, got %d", inner.calls)
	}
}

func TestDedupTool_BlocksRepeatedCalls(t *testing.T) {
	inner := &fakeTool{name: "web_fetch"}
	tracker := newToolCallTracker()
	dt := &dedupTool{inner: inner, tracker: tracker}
	ctx := context.Background()
	args := `{"url":"https://example.com/same"}`

	for i := 1; i <= maxIdenticalToolCalls; i++ {
		res, err := dt.InvokableRun(ctx, args)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if strings.Contains(res, `"error"`) {
			t.Fatalf("call %d: should NOT be blocked yet: %s", i, res)
		}
	}

	if inner.calls != maxIdenticalToolCalls {
		t.Errorf("expected %d inner calls before block, got %d", maxIdenticalToolCalls, inner.calls)
	}

	// Next call should be blocked.
	res, err := dt.InvokableRun(ctx, args)
	if err != nil {
		t.Fatalf("blocked call: unexpected error: %v", err)
	}
	if !strings.Contains(res, `"error"`) {
		t.Fatalf("expected blocked response, got: %s", res)
	}
	if !strings.Contains(res, "do NOT") {
		t.Errorf("expected guidance in blocked response, got: %s", res)
	}
	// Inner tool should NOT have been called again.
	if inner.calls != maxIdenticalToolCalls {
		t.Errorf("inner tool should not be called after block; got %d calls", inner.calls)
	}
}

func TestWrapToolsWithDedup(t *testing.T) {
	inner := &fakeTool{name: "test_tool"}
	wrapped := wrapToolsWithDedup([]tool.BaseTool{inner})
	if len(wrapped) != 1 {
		t.Fatalf("expected 1 wrapped tool, got %d", len(wrapped))
	}

	info, err := wrapped[0].Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if info.Name != "test_tool" {
		t.Errorf("expected tool name 'test_tool', got %q", info.Name)
	}
}
