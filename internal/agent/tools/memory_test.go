package tools

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/open-nipper/open-nipper/internal/agent/memory"
)

func newTestMemStore(t *testing.T) *memory.Store {
	t.Helper()
	return memory.NewStore(t.TempDir(), "test-user", zap.NewNop())
}

func TestMemoryWriteTool(t *testing.T) {
	store := newTestMemStore(t)
	executor := NewMemoryToolExecutor(store)

	result, err := executor.ExecMemoryWrite(context.Background(), MemoryWriteParams{
		Content: "user prefers dark mode",
	})
	if err != nil {
		t.Fatalf("ExecMemoryWrite error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success=true, message=%s", result.Message)
	}
}

func TestMemoryWriteToolEmpty(t *testing.T) {
	store := newTestMemStore(t)
	executor := NewMemoryToolExecutor(store)

	_, err := executor.ExecMemoryWrite(context.Background(), MemoryWriteParams{Content: ""})
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestMemoryWriteToolWhitespace(t *testing.T) {
	store := newTestMemStore(t)
	executor := NewMemoryToolExecutor(store)

	_, err := executor.ExecMemoryWrite(context.Background(), MemoryWriteParams{Content: "   "})
	if err == nil {
		t.Error("expected error for whitespace-only content")
	}
}

func TestMemoryReadToolEmpty(t *testing.T) {
	store := newTestMemStore(t)
	executor := NewMemoryToolExecutor(store)

	result, err := executor.ExecMemoryRead(context.Background(), MemoryReadParams{})
	if err != nil {
		t.Fatalf("ExecMemoryRead error: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 entries, got %d", result.Count)
	}
}

func TestMemoryWriteAndRead(t *testing.T) {
	store := newTestMemStore(t)
	executor := NewMemoryToolExecutor(store)

	_, err := executor.ExecMemoryWrite(context.Background(), MemoryWriteParams{
		Content: "project uses PostgreSQL 15",
	})
	if err != nil {
		t.Fatalf("write error: %v", err)
	}

	result, err := executor.ExecMemoryRead(context.Background(), MemoryReadParams{
		Query: "PostgreSQL",
		Days:  7,
	})
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if result.Count == 0 {
		t.Fatal("expected entries")
	}
	if !strings.Contains(result.Entries[0].Content, "PostgreSQL") {
		t.Errorf("expected PostgreSQL in content: %s", result.Entries[0].Content)
	}
}

func TestMemoryReadDefaultDays(t *testing.T) {
	store := newTestMemStore(t)
	executor := NewMemoryToolExecutor(store)

	_, _ = executor.ExecMemoryWrite(context.Background(), MemoryWriteParams{Content: "today's note"})

	result, err := executor.ExecMemoryRead(context.Background(), MemoryReadParams{Days: 0})
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if result.Count == 0 {
		t.Error("expected entries with default days")
	}
}
