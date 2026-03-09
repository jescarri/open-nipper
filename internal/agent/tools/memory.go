package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-nipper/open-nipper/internal/agent/memory"
)

// MemoryWriteParams defines the input for the memory_write tool.
type MemoryWriteParams struct {
	Content string `json:"content" jsonschema:"description=The fact or note to save to durable memory. Be concise and factual.,required"`
}

// MemoryWriteResult is the output of the memory_write tool.
type MemoryWriteResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// MemoryReadParams defines the input for the memory_read tool.
type MemoryReadParams struct {
	Query string `json:"query" jsonschema:"description=Search query to filter memories (optional — leave empty to return all recent memories)"`
	Days  int    `json:"days,omitempty" jsonschema:"description=Number of days to search back (default 7)"`
}

// MemoryReadResult is the output of the memory_read tool.
type MemoryReadResult struct {
	Entries []memory.Entry `json:"entries"`
	Count   int            `json:"count"`
}

// MemoryToolExecutor holds the memory store reference.
type MemoryToolExecutor struct {
	store *memory.Store
}

// NewMemoryToolExecutor creates an executor backed by the given memory store.
func NewMemoryToolExecutor(store *memory.Store) *MemoryToolExecutor {
	return &MemoryToolExecutor{store: store}
}

// ExecMemoryWrite saves a note to durable memory.
func (e *MemoryToolExecutor) ExecMemoryWrite(_ context.Context, params MemoryWriteParams) (*MemoryWriteResult, error) {
	if strings.TrimSpace(params.Content) == "" {
		return nil, fmt.Errorf("content is required")
	}

	if err := e.store.Write(params.Content); err != nil {
		return &MemoryWriteResult{
			Success: false,
			Message: fmt.Sprintf("Failed to write memory: %v", err),
		}, nil
	}

	return &MemoryWriteResult{
		Success: true,
		Message: "Memory saved successfully.",
	}, nil
}

// ExecMemoryRead searches durable memory.
func (e *MemoryToolExecutor) ExecMemoryRead(_ context.Context, params MemoryReadParams) (*MemoryReadResult, error) {
	days := params.Days
	if days <= 0 {
		days = 7
	}

	entries, err := e.store.Read(params.Query, days)
	if err != nil {
		return nil, fmt.Errorf("reading memory: %w", err)
	}

	return &MemoryReadResult{
		Entries: entries,
		Count:   len(entries),
	}, nil
}
