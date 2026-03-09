package session_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/open-nipper/open-nipper/pkg/session"
)

func newStore(t *testing.T) *session.Store {
	t.Helper()
	return session.NewStore(t.TempDir(), zaptest.NewLogger(t))
}

func makeReq(userID, channelType string) session.CreateSessionRequest {
	return session.CreateSessionRequest{
		UserID:      userID,
		ChannelType: channelType,
		Model:       "claude-test",
	}
}

// ---- CreateSession tests ----------------------------------------------------

func TestCreateSession_CreatesFiles(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess, err := store.CreateSession(ctx, makeReq("alice", "slack"))
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}
	if sess == nil {
		t.Fatal("expected non-nil session")
	}
	if sess.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if sess.Key == "" {
		t.Error("expected non-empty session key")
	}
	if sess.Status != session.StatusActive {
		t.Errorf("expected status=active, got %q", sess.Status)
	}
	if sess.Metadata.Model != "claude-test" {
		t.Errorf("expected model=claude-test, got %q", sess.Metadata.Model)
	}
}

func TestCreateSession_WithExplicitSessionID(t *testing.T) {
	store := newStore(t)
	req := makeReq("bob", "whatsapp")
	req.SessionID = "my-custom-id"

	sess, err := store.CreateSession(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}
	if sess.ID != "my-custom-id" {
		t.Errorf("expected session ID = my-custom-id, got %q", sess.ID)
	}
	expectedKey := session.BuildSessionKey("bob", "whatsapp", "my-custom-id")
	if sess.Key != expectedKey {
		t.Errorf("expected key %q, got %q", expectedKey, sess.Key)
	}
}

func TestCreateSession_MissingUserIDReturnsError(t *testing.T) {
	store := newStore(t)
	_, err := store.CreateSession(context.Background(), session.CreateSessionRequest{
		ChannelType: "slack",
	})
	if err == nil {
		t.Fatal("expected error for missing userID")
	}
}

func TestCreateSession_KeyMatchesParsed(t *testing.T) {
	store := newStore(t)
	sess, err := store.CreateSession(context.Background(), makeReq("carol", "mqtt"))
	if err != nil {
		t.Fatal(err)
	}
	uid, ct, sid, err := session.ParseSessionKey(sess.Key)
	if err != nil {
		t.Fatalf("ParseSessionKey error: %v", err)
	}
	if uid != "carol" || ct != "mqtt" || sid != sess.ID {
		t.Errorf("key components mismatch: uid=%q ct=%q sid=%q", uid, ct, sid)
	}
}

// ---- GetSession tests -------------------------------------------------------

func TestGetSession_ReturnsStoredData(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	created, _ := store.CreateSession(ctx, makeReq("alice", "slack"))
	loaded, err := store.GetSession(ctx, created.Key)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if loaded.ID != created.ID {
		t.Errorf("expected ID %q, got %q", created.ID, loaded.ID)
	}
	if loaded.UserID != "alice" {
		t.Errorf("expected userId=alice, got %q", loaded.UserID)
	}
}

func TestGetSession_NotFoundReturnsError(t *testing.T) {
	store := newStore(t)
	key := session.BuildSessionKey("alice", "slack", "nonexistent")
	_, err := store.GetSession(context.Background(), key)
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestGetSession_InvalidKeyReturnsError(t *testing.T) {
	store := newStore(t)
	_, err := store.GetSession(context.Background(), "not-a-valid-key")
	if err == nil {
		t.Fatal("expected error for invalid session key")
	}
}

// ---- ListSessions tests -----------------------------------------------------

func TestListSessions_ReturnsAllSessions(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	store.CreateSession(ctx, makeReq("dave", "slack"))
	store.CreateSession(ctx, makeReq("dave", "whatsapp"))

	sessions, err := store.ListSessions(ctx, "dave")
	if err != nil {
		t.Fatalf("ListSessions error: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestListSessions_EmptyForUnknownUser(t *testing.T) {
	store := newStore(t)
	sessions, err := store.ListSessions(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessions == nil {
		t.Error("expected non-nil empty slice")
	}
}

// ---- Transcript tests -------------------------------------------------------

func TestAppendAndLoadTranscript(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("eve", "slack"))

	lines := []session.TranscriptLine{
		{Role: "user", Content: "Hello!", Timestamp: time.Now().UTC()},
		{Role: "assistant", Content: "Hi there!", Timestamp: time.Now().UTC()},
	}
	for _, l := range lines {
		if err := store.AppendTranscript(ctx, sess.Key, l); err != nil {
			t.Fatalf("AppendTranscript error: %v", err)
		}
	}

	loaded, err := store.LoadTranscript(ctx, sess.Key)
	if err != nil {
		t.Fatalf("LoadTranscript error: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(loaded))
	}
	if loaded[0].Role != "user" || loaded[0].Content != "Hello!" {
		t.Errorf("unexpected first line: %+v", loaded[0])
	}
	if loaded[1].Role != "assistant" {
		t.Errorf("unexpected second line: %+v", loaded[1])
	}
}

func TestLoadTranscript_EmptySessionIsOK(t *testing.T) {
	store := newStore(t)
	sess, _ := store.CreateSession(context.Background(), makeReq("frank", "mqtt"))

	lines, err := store.LoadTranscript(context.Background(), sess.Key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("expected empty transcript, got %d lines", len(lines))
	}
}

func TestLoadTranscript_NotFoundReturnsError(t *testing.T) {
	store := newStore(t)
	key := session.BuildSessionKey("alice", "slack", "nonexistent")
	_, err := store.LoadTranscript(context.Background(), key)
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestAppendTranscript_ConcurrentWriteIsSafe(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	sess, _ := store.CreateSession(ctx, makeReq("grace", "slack"))

	const n = 20
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			line := session.TranscriptLine{
				Role:      "user",
				Content:   strings.Repeat("x", i+1),
				Timestamp: time.Now().UTC(),
			}
			done <- store.AppendTranscript(ctx, sess.Key, line)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent append error: %v", err)
		}
	}

	lines, err := store.LoadTranscript(ctx, sess.Key)
	if err != nil {
		t.Fatalf("LoadTranscript error: %v", err)
	}
	if len(lines) != n {
		t.Errorf("expected %d lines after concurrent appends, got %d", n, len(lines))
	}
}

// ---- UpdateMeta tests -------------------------------------------------------

func TestUpdateMeta_PersistsMeta(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("henry", "slack"))

	meta := sess.Metadata
	meta.MessageCount = 7
	meta.Model = "claude-new"

	if err := store.UpdateMeta(ctx, sess.Key, meta); err != nil {
		t.Fatalf("UpdateMeta error: %v", err)
	}

	loaded, err := store.GetSession(ctx, sess.Key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Metadata.MessageCount != 7 {
		t.Errorf("expected MessageCount=7, got %d", loaded.Metadata.MessageCount)
	}
	if loaded.Metadata.Model != "claude-new" {
		t.Errorf("expected model=claude-new, got %q", loaded.Metadata.Model)
	}
}

func TestUpdateMeta_NotFoundReturnsError(t *testing.T) {
	store := newStore(t)
	key := session.BuildSessionKey("alice", "slack", "missing")
	err := store.UpdateMeta(context.Background(), key, session.SessionMetadata{})
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

// ---- ArchiveSession tests ---------------------------------------------------

func TestArchiveSession_MarksArchived(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("ivan", "whatsapp"))
	_ = store.AppendTranscript(ctx, sess.Key, session.TranscriptLine{
		Role: "user", Content: "test", Timestamp: time.Now(),
	})

	if err := store.ArchiveSession(ctx, sess.Key); err != nil {
		t.Fatalf("ArchiveSession error: %v", err)
	}

	loaded, err := store.GetSession(ctx, sess.Key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != session.StatusArchived {
		t.Errorf("expected status=archived, got %q", loaded.Status)
	}
}

func TestArchiveSession_TranscriptNotLoadableAfterArchive(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("jake", "whatsapp"))
	_ = store.AppendTranscript(ctx, sess.Key, session.TranscriptLine{
		Role: "user", Content: "msg", Timestamp: time.Now(),
	})
	_ = store.ArchiveSession(ctx, sess.Key)

	_, err := store.LoadTranscript(ctx, sess.Key)
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after archive, got %v", err)
	}
}

// ---- ResetSession tests -----------------------------------------------------

func TestResetSession_CreatesNewSession(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("kate", "slack"))
	newKey, err := store.ResetSession(ctx, sess.Key)
	if err != nil {
		t.Fatalf("ResetSession error: %v", err)
	}
	if newKey == sess.Key {
		t.Error("expected new session key to differ from old")
	}
	if !strings.HasPrefix(newKey, "user:kate:channel:slack:session:") {
		t.Errorf("expected new key for same user+channel, got %q", newKey)
	}
}

func TestResetSession_OldSessionIsArchived(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sess, _ := store.CreateSession(ctx, makeReq("leo", "slack"))
	_, _ = store.ResetSession(ctx, sess.Key)

	loaded, err := store.GetSession(ctx, sess.Key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != session.StatusArchived {
		t.Errorf("expected old session status=archived, got %q", loaded.Status)
	}
}

// ---- UpdateIndex tests ------------------------------------------------------

func TestUpdateIndex_RebuildFromMetaFiles(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	base := t.TempDir()
	store2 := session.NewStore(base, zaptest.NewLogger(t))

	store2.CreateSession(ctx, makeReq("mia", "slack"))
	store2.CreateSession(ctx, makeReq("mia", "whatsapp"))

	if err := store2.UpdateIndex(ctx, "mia"); err != nil {
		t.Fatalf("UpdateIndex error: %v", err)
	}

	sessions, err := store2.ListSessions(ctx, "mia")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions after rebuild, got %d", len(sessions))
	}
	_ = store
	_ = filepath.Join(base, "users", "mia", "sessions")
}
