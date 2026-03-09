// Package memory provides a file-based durable memory store for agents.
// Memories are stored as daily Markdown files under:
//
//	{basePath}/users/{userId}/memory/YYYY-MM-DD.md
//
// Agents write facts, notes, and observations into memory during conversations.
// Memory content is injected into the system prompt to give the agent long-term
// recall across sessions.
package memory

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Entry represents a single memory item read from disk.
type Entry struct {
	Date    string `json:"date"`
	Content string `json:"content"`
}

// Store manages durable memory files on disk.
type Store struct {
	memoryDir string
	logger    *zap.Logger
}

// NewStore creates a Store that reads/writes memory files under
// {basePath}/users/{userID}/memory/.
func NewStore(basePath, userID string, logger *zap.Logger) *Store {
	return &Store{
		memoryDir: filepath.Join(basePath, "users", userID, "memory"),
		logger:    logger,
	}
}

// Write appends content to today's memory file.
func (s *Store) Write(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("memory content is empty")
	}

	if err := os.MkdirAll(s.memoryDir, 0700); err != nil {
		return fmt.Errorf("creating memory directory: %w", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	filePath := filepath.Join(s.memoryDir, today+".md")

	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("opening memory file: %w", err)
	}
	defer f.Close()

	timestamp := time.Now().UTC().Format("15:04:05Z")
	entry := fmt.Sprintf("- [%s] %s\n", timestamp, strings.TrimSpace(content))

	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("writing memory entry: %w", err)
	}

	s.logger.Debug("memory written",
		zap.String("file", today+".md"),
		zap.Int("contentLen", len(content)),
	)
	return nil
}

// Read returns memory entries from the last `days` days, optionally filtered
// by a case-insensitive substring query. If query is empty all entries are returned.
func (s *Store) Read(query string, days int) ([]Entry, error) {
	if days <= 0 {
		days = 7
	}

	var entries []Entry
	now := time.Now().UTC()
	queryLower := strings.ToLower(strings.TrimSpace(query))

	for i := 0; i < days; i++ {
		date := now.AddDate(0, 0, -i).Format("2006-01-02")
		filePath := filepath.Join(s.memoryDir, date+".md")

		content, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			s.logger.Warn("failed to read memory file", zap.String("file", filePath), zap.Error(err))
			continue
		}

		text := string(content)
		if queryLower != "" {
			var matchedLines []string
			scanner := bufio.NewScanner(strings.NewReader(text))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(strings.ToLower(line), queryLower) {
					matchedLines = append(matchedLines, line)
				}
			}
			if len(matchedLines) == 0 {
				continue
			}
			text = strings.Join(matchedLines, "\n")
		}

		entries = append(entries, Entry{
			Date:    date,
			Content: strings.TrimSpace(text),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Date > entries[j].Date
	})

	return entries, nil
}

// Inject builds a memory context string suitable for injection into the system
// prompt. It reads the last `days` days of memory, capped at maxBytes.
func (s *Store) Inject(days int, maxBytes int) string {
	if days <= 0 {
		days = 7
	}
	if maxBytes <= 0 {
		maxBytes = 4000
	}

	entries, err := s.Read("", days)
	if err != nil || len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Durable Memory\n\n")
	sb.WriteString("The following notes were saved from previous conversations:\n\n")

	for _, e := range entries {
		section := fmt.Sprintf("### %s\n%s\n\n", e.Date, e.Content)
		if sb.Len()+len(section) > maxBytes {
			remaining := maxBytes - sb.Len()
			if remaining > 50 {
				sb.WriteString(section[:remaining])
				sb.WriteString("\n[memory truncated]")
			}
			break
		}
		sb.WriteString(section)
	}

	return sb.String()
}

// MemoryDir returns the directory path for testing purposes.
func (s *Store) MemoryDir() string {
	return s.memoryDir
}
