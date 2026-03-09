package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ErrSessionNotFound is returned when a session key does not correspond to an
// existing session on disk.
var ErrSessionNotFound = errors.New("session not found")

// SessionStore defines the session management operations available to agents.
type SessionStore interface {
	CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error)
	GetSession(ctx context.Context, sessionKey string) (*Session, error)
	ListSessions(ctx context.Context, userID string) ([]*Session, error)
	LoadTranscript(ctx context.Context, sessionKey string) ([]TranscriptLine, error)
	AppendTranscript(ctx context.Context, sessionKey string, line TranscriptLine) error
	UpdateMeta(ctx context.Context, sessionKey string, meta SessionMetadata) error
	UpdateIndex(ctx context.Context, userID string) error
	ArchiveSession(ctx context.Context, sessionKey string) error
	ResetSession(ctx context.Context, sessionKey string) (string, error)
}

type sessionIndex struct {
	UserID    string               `json:"userId"`
	Sessions  []*sessionIndexEntry `json:"sessions"`
	UpdatedAt time.Time            `json:"updatedAt"`
}

type sessionIndexEntry struct {
	SessionID    string        `json:"sessionId"`
	SessionKey   string        `json:"sessionKey"`
	ChannelType  string        `json:"channelType"`
	Status       SessionStatus `json:"status"`
	Model        string        `json:"model"`
	MessageCount int           `json:"messageCount"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
}

type indexCacheEntry struct {
	index     sessionIndex
	expiresAt time.Time
}

const indexCacheTTL = 45 * time.Second

// Store is the filesystem-backed session store.
type Store struct {
	basePath string
	logger   *zap.Logger

	mu         sync.RWMutex
	indexCache map[string]*indexCacheEntry
}

// NewStore creates a Store rooted at basePath (e.g. ~/.open-nipper).
func NewStore(basePath string, logger *zap.Logger) *Store {
	return &Store{
		basePath:   basePath,
		logger:     logger,
		indexCache: make(map[string]*indexCacheEntry),
	}
}

func (s *Store) userDir(userID string) string {
	return filepath.Join(s.basePath, "users", userID)
}

func (s *Store) sessionsDir(userID string) string {
	return filepath.Join(s.userDir(userID), "sessions")
}

func (s *Store) archiveDir(userID string) string {
	return filepath.Join(s.sessionsDir(userID), "archive")
}

func (s *Store) transcriptPath(userID, sessionID string) string {
	return filepath.Join(s.sessionsDir(userID), sessionID+".jsonl")
}

func (s *Store) lockPath(userID, sessionID string) string {
	return filepath.Join(s.sessionsDir(userID), sessionID+".jsonl.lock")
}

func (s *Store) metaPath(userID, sessionID string) string {
	return filepath.Join(s.sessionsDir(userID), sessionID+".meta.json")
}

func (s *Store) indexPath(userID string) string {
	return filepath.Join(s.sessionsDir(userID), "sessions.json")
}

// CreateSession atomically creates all session files and updates the index.
func (s *Store) CreateSession(_ context.Context, req CreateSessionRequest) (*Session, error) {
	if req.UserID == "" {
		return nil, fmt.Errorf("CreateSession: userID is required")
	}
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	sessDir := s.sessionsDir(req.UserID)
	if err := os.MkdirAll(sessDir, 0700); err != nil {
		return nil, fmt.Errorf("CreateSession: mkdir sessions dir: %w", err)
	}
	if err := os.MkdirAll(s.archiveDir(req.UserID), 0700); err != nil {
		return nil, fmt.Errorf("CreateSession: mkdir archive dir: %w", err)
	}

	now := time.Now().UTC()
	sessionKey := BuildSessionKey(req.UserID, req.ChannelType, sessionID)

	sess := &Session{
		Key:         sessionKey,
		ID:          sessionID,
		UserID:      req.UserID,
		ChannelType: req.ChannelType,
		Status:      StatusActive,
		Metadata: SessionMetadata{
			Model:          req.Model,
			MessageCount:   0,
			LastActivityAt: now,
			ChannelMeta:    req.ChannelMeta,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := atomicWriteJSON(s.metaPath(req.UserID, sessionID), sess); err != nil {
		return nil, fmt.Errorf("CreateSession: write meta: %w", err)
	}

	tf, err := os.OpenFile(s.transcriptPath(req.UserID, sessionID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("CreateSession: create transcript: %w", err)
	}
	if tf != nil {
		_ = tf.Close()
	}

	if err := s.upsertIndex(req.UserID, sess); err != nil {
		return nil, fmt.Errorf("CreateSession: update index: %w", err)
	}

	s.logger.Info("session created",
		zap.String("userId", req.UserID),
		zap.String("sessionKey", sessionKey),
		zap.String("channelType", req.ChannelType),
	)
	return sess, nil
}

// GetSession loads the session metadata from the meta.json file.
func (s *Store) GetSession(_ context.Context, sessionKey string) (*Session, error) {
	userID, _, sessionID, err := ParseSessionKey(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("GetSession: %w", err)
	}

	data, err := os.ReadFile(s.metaPath(userID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("GetSession: read meta: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("GetSession: decode meta: %w", err)
	}
	return &sess, nil
}

// ListSessions returns all sessions for a user, reading from the index (with TTL cache).
func (s *Store) ListSessions(_ context.Context, userID string) ([]*Session, error) {
	idx, err := s.loadIndex(userID)
	if err != nil {
		return nil, fmt.Errorf("ListSessions: %w", err)
	}

	out := make([]*Session, 0, len(idx.Sessions))
	for _, e := range idx.Sessions {
		out = append(out, indexEntryToSession(e))
	}
	return out, nil
}

// LoadTranscript reads all lines from the JSONL transcript file.
func (s *Store) LoadTranscript(_ context.Context, sessionKey string) ([]TranscriptLine, error) {
	userID, _, sessionID, err := ParseSessionKey(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("LoadTranscript: %w", err)
	}

	f, err := os.Open(s.transcriptPath(userID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("LoadTranscript: open: %w", err)
	}
	defer f.Close()

	var lines []TranscriptLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var tl TranscriptLine
		if err := json.Unmarshal([]byte(line), &tl); err != nil {
			s.logger.Warn("skipping malformed transcript line",
				zap.String("sessionKey", sessionKey),
				zap.Error(err),
			)
			continue
		}
		lines = append(lines, tl)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("LoadTranscript: scan: %w", err)
	}
	return lines, nil
}

// AppendTranscript appends a single line to the transcript under a file lock.
func (s *Store) AppendTranscript(_ context.Context, sessionKey string, line TranscriptLine) error {
	userID, _, sessionID, err := ParseSessionKey(sessionKey)
	if err != nil {
		return fmt.Errorf("AppendTranscript: %w", err)
	}

	lock := NewFileLock(s.lockPath(userID, sessionID))
	if err := lock.Lock(context.Background(), userID); err != nil {
		return fmt.Errorf("AppendTranscript: acquire lock: %w", err)
	}
	defer lock.Unlock()

	data, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("AppendTranscript: marshal: %w", err)
	}

	f, err := os.OpenFile(s.transcriptPath(userID, sessionID), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("AppendTranscript: open transcript: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("AppendTranscript: write: %w", err)
	}
	return nil
}

// UpdateMeta atomically replaces the session metadata and refreshes the index entry.
func (s *Store) UpdateMeta(_ context.Context, sessionKey string, meta SessionMetadata) error {
	userID, chanType, sessionID, err := ParseSessionKey(sessionKey)
	if err != nil {
		return fmt.Errorf("UpdateMeta: %w", err)
	}

	data, err := os.ReadFile(s.metaPath(userID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("UpdateMeta: read meta: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return fmt.Errorf("UpdateMeta: decode meta: %w", err)
	}

	sess.Metadata = meta
	sess.UpdatedAt = time.Now().UTC()

	if err := atomicWriteJSON(s.metaPath(userID, sessionID), &sess); err != nil {
		return fmt.Errorf("UpdateMeta: write meta: %w", err)
	}

	_ = s.upsertIndex(userID, &sess)

	s.logger.Debug("session metadata updated",
		zap.String("userId", userID),
		zap.String("channelType", chanType),
		zap.String("sessionId", sessionID),
	)
	return nil
}

// UpdateIndex rebuilds sessions.json by scanning all meta files in the user's
// sessions directory.
func (s *Store) UpdateIndex(_ context.Context, userID string) error {
	return s.rebuildIndex(userID)
}

// ArchiveSession moves the transcript to the archive directory and marks the
// session as archived in its metadata.
func (s *Store) ArchiveSession(ctx context.Context, sessionKey string) error {
	userID, _, sessionID, err := ParseSessionKey(sessionKey)
	if err != nil {
		return fmt.Errorf("ArchiveSession: %w", err)
	}

	lock := NewFileLock(s.lockPath(userID, sessionID))
	if err := lock.Lock(ctx, userID); err != nil {
		return fmt.Errorf("ArchiveSession: acquire lock: %w", err)
	}
	defer lock.Unlock()

	ts := time.Now().UTC().Format("20060102T150405Z")
	archiveName := fmt.Sprintf("%s-%s.jsonl", sessionID, ts)
	archivePath := filepath.Join(s.archiveDir(userID), archiveName)

	if err := os.MkdirAll(s.archiveDir(userID), 0700); err != nil {
		return fmt.Errorf("ArchiveSession: mkdir archive: %w", err)
	}

	src := s.transcriptPath(userID, sessionID)
	if err := os.Rename(src, archivePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("ArchiveSession: move transcript: %w", err)
	}

	metaData, err := os.ReadFile(s.metaPath(userID, sessionID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("ArchiveSession: read meta: %w", err)
	}
	if err == nil {
		var sess Session
		if jsonErr := json.Unmarshal(metaData, &sess); jsonErr == nil {
			sess.Status = StatusArchived
			sess.UpdatedAt = time.Now().UTC()
			_ = atomicWriteJSON(s.metaPath(userID, sessionID), &sess)
			_ = s.upsertIndex(userID, &sess)
		}
	}

	s.logger.Info("session archived",
		zap.String("userId", userID),
		zap.String("sessionId", sessionID),
		zap.String("archivePath", archivePath),
	)
	return nil
}

// ResetSession archives the current session transcript and creates a new session
// with a fresh ID. Returns the new session key.
func (s *Store) ResetSession(ctx context.Context, sessionKey string) (string, error) {
	userID, chanType, _, err := ParseSessionKey(sessionKey)
	if err != nil {
		return "", fmt.Errorf("ResetSession: %w", err)
	}

	old, err := s.GetSession(ctx, sessionKey)
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		return "", fmt.Errorf("ResetSession: load old session: %w", err)
	}

	if err := s.ArchiveSession(ctx, sessionKey); err != nil {
		return "", fmt.Errorf("ResetSession: archive: %w", err)
	}

	req := CreateSessionRequest{
		UserID:      userID,
		ChannelType: chanType,
	}
	if old != nil {
		req.Model = old.Metadata.Model
		req.ChannelMeta = old.Metadata.ChannelMeta
	}

	newSess, err := s.CreateSession(ctx, req)
	if err != nil {
		return "", fmt.Errorf("ResetSession: create new session: %w", err)
	}

	s.logger.Info("session reset",
		zap.String("userId", userID),
		zap.String("oldSessionKey", sessionKey),
		zap.String("newSessionKey", newSess.Key),
	)
	return newSess.Key, nil
}

// --- Index helpers ---

func (s *Store) loadIndex(userID string) (*sessionIndex, error) {
	s.mu.RLock()
	entry, ok := s.indexCache[userID]
	s.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		idx := entry.index
		return &idx, nil
	}

	return s.refreshIndexCache(userID)
}

func (s *Store) refreshIndexCache(userID string) (*sessionIndex, error) {
	data, err := os.ReadFile(s.indexPath(userID))
	if err != nil {
		if os.IsNotExist(err) {
			return &sessionIndex{UserID: userID}, nil
		}
		return nil, fmt.Errorf("read session index: %w", err)
	}
	var idx sessionIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("decode session index: %w", err)
	}

	s.mu.Lock()
	s.indexCache[userID] = &indexCacheEntry{
		index:     idx,
		expiresAt: time.Now().Add(indexCacheTTL),
	}
	s.mu.Unlock()
	return &idx, nil
}

func (s *Store) upsertIndex(userID string, sess *Session) error {
	s.mu.Lock()
	delete(s.indexCache, userID)
	s.mu.Unlock()

	idx, err := s.refreshIndexCache(userID)
	if err != nil {
		return err
	}

	entry := sessionToIndexEntry(sess)
	updated := false
	for i, e := range idx.Sessions {
		if e.SessionID == sess.ID {
			idx.Sessions[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		idx.Sessions = append(idx.Sessions, entry)
	}
	idx.UpdatedAt = time.Now().UTC()
	if idx.UserID == "" {
		idx.UserID = userID
	}

	if err := atomicWriteJSON(s.indexPath(userID), idx); err != nil {
		return fmt.Errorf("write session index: %w", err)
	}

	s.mu.Lock()
	s.indexCache[userID] = &indexCacheEntry{
		index:     *idx,
		expiresAt: time.Now().Add(indexCacheTTL),
	}
	s.mu.Unlock()
	return nil
}

func (s *Store) rebuildIndex(userID string) error {
	s.mu.Lock()
	delete(s.indexCache, userID)
	s.mu.Unlock()

	sessDir := s.sessionsDir(userID)
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("rebuild index: read dir: %w", err)
	}

	idx := sessionIndex{
		UserID:    userID,
		UpdatedAt: time.Now().UTC(),
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		path := filepath.Join(sessDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			s.logger.Warn("skip unreadable meta file", zap.String("path", path), zap.Error(err))
			continue
		}
		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			s.logger.Warn("skip malformed meta file", zap.String("path", path), zap.Error(err))
			continue
		}
		idx.Sessions = append(idx.Sessions, sessionToIndexEntry(&sess))
	}

	if err := atomicWriteJSON(s.indexPath(userID), &idx); err != nil {
		return fmt.Errorf("rebuild index: write: %w", err)
	}
	return nil
}

// --- Utility functions ---

func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON for %q: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write temp file %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %q → %q: %w", tmp, path, err)
	}
	return nil
}

func sessionToIndexEntry(sess *Session) *sessionIndexEntry {
	return &sessionIndexEntry{
		SessionID:    sess.ID,
		SessionKey:   sess.Key,
		ChannelType:  sess.ChannelType,
		Status:       sess.Status,
		Model:        sess.Metadata.Model,
		MessageCount: sess.Metadata.MessageCount,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
	}
}

func indexEntryToSession(e *sessionIndexEntry) *Session {
	return &Session{
		Key:         e.SessionKey,
		ID:          e.SessionID,
		ChannelType: e.ChannelType,
		Status:      e.Status,
		Metadata: SessionMetadata{
			Model:        e.Model,
			MessageCount: e.MessageCount,
		},
		CreatedAt: e.CreatedAt,
		UpdatedAt: e.UpdatedAt,
	}
}
