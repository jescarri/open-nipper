package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// Compactor trims session transcripts that grow beyond a configured threshold.
//
// Compaction guarantees:
//   - Archived lines are written atomically to the archive directory before the
//     active transcript is rewritten — no data loss even if the process crashes.
//   - The active transcript is rewritten atomically (write to temp, rename).
//   - Session metadata is updated with the new compaction count and message count.
type Compactor struct {
	store  *Store
	logger *zap.Logger
}

// NewCompactor creates a Compactor backed by store.
func NewCompactor(store *Store, logger *zap.Logger) *Compactor {
	return &Compactor{store: store, logger: logger}
}

// CompactionResult describes the outcome of a Compact call.
type CompactionResult struct {
	OriginalLineCount  int
	ArchivedLineCount  int
	RemainingLineCount int
	CompactionCount    int
	Compacted          bool
}

// Compact trims the transcript to the keepLines most-recent entries, archiving
// the older portion. It is a no-op when len(transcript) <= keepLines.
//
// keepLines must be at least 1.
func (c *Compactor) Compact(ctx context.Context, sessionKey string, keepLines int) (*CompactionResult, error) {
	if keepLines < 1 {
		return nil, fmt.Errorf("Compact: keepLines must be >= 1, got %d", keepLines)
	}

	lines, err := c.store.LoadTranscript(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("Compact: load transcript: %w", err)
	}

	result := &CompactionResult{
		OriginalLineCount:  len(lines),
		RemainingLineCount: len(lines),
	}

	if len(lines) <= keepLines {
		return result, nil
	}

	splitAt := len(lines) - keepLines
	toArchive := lines[:splitAt]
	toKeep := lines[splitAt:]

	userID, _, sessionID, err := ParseSessionKey(sessionKey)
	if err != nil {
		return nil, fmt.Errorf("Compact: parse key: %w", err)
	}

	if err := c.writeCompactionArchive(userID, sessionID, toArchive); err != nil {
		return nil, fmt.Errorf("Compact: write archive: %w", err)
	}

	if err := c.rewriteTranscript(ctx, userID, sessionID, toKeep); err != nil {
		return nil, fmt.Errorf("Compact: rewrite transcript: %w", err)
	}

	sess, err := c.store.GetSession(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("Compact: get session: %w", err)
	}
	sess.Metadata.CompactionCount++
	sess.Metadata.MessageCount = len(toKeep)
	if err := c.store.UpdateMeta(ctx, sessionKey, sess.Metadata); err != nil {
		return nil, fmt.Errorf("Compact: update meta: %w", err)
	}

	result.ArchivedLineCount = len(toArchive)
	result.RemainingLineCount = len(toKeep)
	result.CompactionCount = sess.Metadata.CompactionCount
	result.Compacted = true

	c.logger.Info("session compacted",
		zap.String("userId", userID),
		zap.String("sessionId", sessionID),
		zap.Int("archivedLines", len(toArchive)),
		zap.Int("remainingLines", len(toKeep)),
		zap.Int("compactionCount", sess.Metadata.CompactionCount),
	)
	return result, nil
}

// ShouldCompact returns true if the transcript has more lines than maxLines.
func ShouldCompact(lineCount, maxLines int) bool {
	return lineCount > maxLines
}

func (c *Compactor) writeCompactionArchive(userID, sessionID string, lines []TranscriptLine) error {
	archDir := c.store.archiveDir(userID)
	if err := os.MkdirAll(archDir, 0700); err != nil {
		return fmt.Errorf("mkdir archive dir: %w", err)
	}

	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	archPath := filepath.Join(archDir, fmt.Sprintf("%s-compact-%s.jsonl", sessionID, ts))

	f, err := os.OpenFile(archPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	defer f.Close()

	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("marshal transcript line: %w", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("write archive line: %w", err)
		}
	}
	return nil
}

func (c *Compactor) rewriteTranscript(ctx context.Context, userID, sessionID string, lines []TranscriptLine) error {
	transcriptPath := c.store.transcriptPath(userID, sessionID)
	tmpPath := transcriptPath + ".compact.tmp"

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open temp transcript: %w", err)
	}

	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("marshal line: %w", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("write line: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp transcript: %w", err)
	}

	lockPath := c.store.lockPath(userID, sessionID)
	lock := NewFileLock(lockPath)
	if err := lock.Lock(ctx, userID); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("acquire lock for compaction rename: %w", err)
	}
	defer lock.Unlock()

	if err := os.Rename(tmpPath, transcriptPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename compacted transcript: %w", err)
	}
	return nil
}
