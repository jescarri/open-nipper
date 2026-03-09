package session_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/open-nipper/open-nipper/pkg/session"
)

func newCompactor(t *testing.T) (*session.Compactor, *session.Store) {
	t.Helper()
	store := session.NewStore(t.TempDir(), zaptest.NewLogger(t))
	c := session.NewCompactor(store, zaptest.NewLogger(t))
	return c, store
}

func appendLines(t *testing.T, store *session.Store, key string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		line := session.TranscriptLine{
			Role:      "user",
			Content:   "message",
			Timestamp: time.Now().UTC(),
		}
		if err := store.AppendTranscript(context.Background(), key, line); err != nil {
			t.Fatalf("AppendTranscript error: %v", err)
		}
	}
}

func TestShouldCompact_BelowThreshold(t *testing.T) {
	if session.ShouldCompact(50, 100) {
		t.Error("expected ShouldCompact=false when below threshold")
	}
}

func TestShouldCompact_AtThreshold(t *testing.T) {
	if session.ShouldCompact(100, 100) {
		t.Error("expected ShouldCompact=false when at threshold")
	}
}

func TestShouldCompact_AboveThreshold(t *testing.T) {
	if !session.ShouldCompact(101, 100) {
		t.Error("expected ShouldCompact=true when above threshold")
	}
}

func TestCompact_NoOpWhenBelowThreshold(t *testing.T) {
	c, store := newCompactor(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("alice", "slack"))
	appendLines(t, store, sess.Key, 5)

	result, err := c.Compact(ctx, sess.Key, 10)
	if err != nil {
		t.Fatalf("Compact error: %v", err)
	}
	if result.Compacted {
		t.Error("expected Compacted=false when below threshold")
	}
	if result.OriginalLineCount != 5 {
		t.Errorf("expected OriginalLineCount=5, got %d", result.OriginalLineCount)
	}
}

func TestCompact_TrimsToKeepLines(t *testing.T) {
	c, store := newCompactor(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("bob", "slack"))
	appendLines(t, store, sess.Key, 20)

	result, err := c.Compact(ctx, sess.Key, 8)
	if err != nil {
		t.Fatalf("Compact error: %v", err)
	}
	if !result.Compacted {
		t.Error("expected Compacted=true")
	}
	if result.OriginalLineCount != 20 {
		t.Errorf("expected OriginalLineCount=20, got %d", result.OriginalLineCount)
	}
	if result.ArchivedLineCount != 12 {
		t.Errorf("expected ArchivedLineCount=12, got %d", result.ArchivedLineCount)
	}
	if result.RemainingLineCount != 8 {
		t.Errorf("expected RemainingLineCount=8, got %d", result.RemainingLineCount)
	}
}

func TestCompact_TranscriptHasCorrectLinesAfterCompaction(t *testing.T) {
	c, store := newCompactor(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("carol", "slack"))

	for i := 0; i < 10; i++ {
		_ = store.AppendTranscript(ctx, sess.Key, session.TranscriptLine{
			Role:      "user",
			Content:   "msg",
			Timestamp: time.Now().UTC(),
		})
	}

	_, err := c.Compact(ctx, sess.Key, 4)
	if err != nil {
		t.Fatalf("Compact error: %v", err)
	}

	lines, err := store.LoadTranscript(ctx, sess.Key)
	if err != nil {
		t.Fatalf("LoadTranscript after compact error: %v", err)
	}
	if len(lines) != 4 {
		t.Errorf("expected 4 remaining lines, got %d", len(lines))
	}
}

func TestCompact_IncrementsCompactionCount(t *testing.T) {
	c, store := newCompactor(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("dave", "slack"))
	appendLines(t, store, sess.Key, 10)

	r1, _ := c.Compact(ctx, sess.Key, 5)
	if r1.CompactionCount != 1 {
		t.Errorf("expected CompactionCount=1 after first compact, got %d", r1.CompactionCount)
	}

	appendLines(t, store, sess.Key, 10)
	r2, err := c.Compact(ctx, sess.Key, 5)
	if err != nil {
		t.Fatalf("second Compact error: %v", err)
	}
	if r2.CompactionCount != 2 {
		t.Errorf("expected CompactionCount=2 after second compact, got %d", r2.CompactionCount)
	}
}

func TestCompact_MetadataUpdated(t *testing.T) {
	c, store := newCompactor(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("eve", "slack"))
	appendLines(t, store, sess.Key, 15)

	if _, err := c.Compact(ctx, sess.Key, 6); err != nil {
		t.Fatalf("Compact error: %v", err)
	}

	loaded, err := store.GetSession(ctx, sess.Key)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if loaded.Metadata.CompactionCount != 1 {
		t.Errorf("expected CompactionCount=1, got %d", loaded.Metadata.CompactionCount)
	}
	if loaded.Metadata.MessageCount != 6 {
		t.Errorf("expected MessageCount=6, got %d", loaded.Metadata.MessageCount)
	}
}

func TestCompact_InvalidKeepLinesReturnsError(t *testing.T) {
	_, store := newCompactor(t)
	c := session.NewCompactor(store, zaptest.NewLogger(t))
	sess, _ := store.CreateSession(context.Background(), makeReq("frank", "slack"))

	_, err := c.Compact(context.Background(), sess.Key, 0)
	if err == nil {
		t.Fatal("expected error for keepLines=0")
	}
}

func TestCompact_AppendAfterCompactionWorks(t *testing.T) {
	c, store := newCompactor(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("grace", "slack"))
	appendLines(t, store, sess.Key, 10)

	if _, err := c.Compact(ctx, sess.Key, 5); err != nil {
		t.Fatalf("Compact error: %v", err)
	}

	if err := store.AppendTranscript(ctx, sess.Key, session.TranscriptLine{
		Role: "user", Content: "after compact", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("AppendTranscript after compact error: %v", err)
	}

	lines, err := store.LoadTranscript(ctx, sess.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 6 {
		t.Errorf("expected 6 lines after compact+append, got %d", len(lines))
	}
}
