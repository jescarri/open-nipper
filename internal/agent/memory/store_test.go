package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	logger, _ := zap.NewNop(), error(nil)
	return NewStore(dir, "test-user", logger)
}

func TestWriteCreatesFile(t *testing.T) {
	store := newTestStore(t)

	if err := store.Write("test fact"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	filePath := filepath.Join(store.MemoryDir(), today+".md")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.Contains(string(data), "test fact") {
		t.Errorf("content not found in file: %s", string(data))
	}
}

func TestWriteAppends(t *testing.T) {
	store := newTestStore(t)

	if err := store.Write("fact one"); err != nil {
		t.Fatalf("Write 1 failed: %v", err)
	}
	if err := store.Write("fact two"); err != nil {
		t.Fatalf("Write 2 failed: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	data, err := os.ReadFile(filepath.Join(store.MemoryDir(), today+".md"))
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !strings.Contains(string(data), "fact one") {
		t.Error("fact one not found")
	}
	if !strings.Contains(string(data), "fact two") {
		t.Error("fact two not found")
	}
}

func TestWriteRejectsEmpty(t *testing.T) {
	store := newTestStore(t)
	if err := store.Write(""); err == nil {
		t.Error("expected error for empty content")
	}
	if err := store.Write("   "); err == nil {
		t.Error("expected error for whitespace-only content")
	}
}

func TestReadReturnsEntries(t *testing.T) {
	store := newTestStore(t)

	if err := store.Write("test memory entry"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	entries, err := store.Read("", 7)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries returned")
	}
	if !strings.Contains(entries[0].Content, "test memory entry") {
		t.Errorf("entry content mismatch: %s", entries[0].Content)
	}
}

func TestReadFiltersByQuery(t *testing.T) {
	store := newTestStore(t)

	_ = store.Write("the sky is blue")
	_ = store.Write("cats are pets")
	_ = store.Write("dogs are pets too")

	entries, err := store.Read("pets", 7)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Content, "cats") {
		t.Error("expected cats entry")
	}
	if !strings.Contains(entries[0].Content, "dogs") {
		t.Error("expected dogs entry")
	}
}

func TestReadEmptyDir(t *testing.T) {
	store := newTestStore(t)

	entries, err := store.Read("", 7)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestInjectReturnsEmptyWithNoMemory(t *testing.T) {
	store := newTestStore(t)
	result := store.Inject(7, 4000)
	if result != "" {
		t.Errorf("expected empty injection, got: %s", result)
	}
}

func TestInjectReturnsMemoryContext(t *testing.T) {
	store := newTestStore(t)
	_ = store.Write("user prefers dark mode")

	result := store.Inject(7, 4000)
	if result == "" {
		t.Fatal("expected non-empty injection")
	}
	if !strings.Contains(result, "Durable Memory") {
		t.Error("missing header")
	}
	if !strings.Contains(result, "dark mode") {
		t.Error("missing content")
	}
}

func TestInjectTruncates(t *testing.T) {
	store := newTestStore(t)
	_ = store.Write(strings.Repeat("x", 500))

	result := store.Inject(7, 100)
	if len(result) > 200 {
		t.Errorf("injection not truncated: len=%d", len(result))
	}
}
