// Package llm provides a factory for creating Eino ChatModel instances.
package llm

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ttftKey is the context key for storing TTFT (time-to-first-token) duration.
type ttftKey struct{}

// TTFTFromContext extracts the TTFT duration stored by StreamingGenerateModel.
// Returns zero if not present.
func TTFTFromContext(ctx context.Context) time.Duration {
	if v, ok := ctx.Value(ttftKey{}).(time.Duration); ok {
		return v
	}
	return 0
}

// generationDurationKey is the context key for the total generation (decode) duration.
type generationDurationKey struct{}

// GenerationDurationFromContext extracts the total generation duration
// (first token to last token) stored by StreamingGenerateModel.
// Returns zero if not present.
func GenerationDurationFromContext(ctx context.Context) time.Duration {
	if v, ok := ctx.Value(generationDurationKey{}).(time.Duration); ok {
		return v
	}
	return 0
}

// llmTimingKey stores a pointer to mutable timing data so the model callback
// can read values written during Generate.
type llmTimingKey struct{}

// LLMTiming holds timing measurements captured during a single LLM Generate call.
type LLMTiming struct {
	TTFT               time.Duration // time from request sent to first token received
	GenerationDuration time.Duration // time from first token to last token
}

// LLMTimingFromContext returns the LLMTiming pointer stored in ctx, or nil.
func LLMTimingFromContext(ctx context.Context) *LLMTiming {
	if v, ok := ctx.Value(llmTimingKey{}).(*LLMTiming); ok {
		return v
	}
	return nil
}

// ContextWithLLMTiming returns a context carrying a mutable LLMTiming struct.
// Call this before Generate so the model can populate timing data.
func ContextWithLLMTiming(ctx context.Context, t *LLMTiming) context.Context {
	return context.WithValue(ctx, llmTimingKey{}, t)
}

// ToolCallingChatModel is re-exported for use by callers outside this package.
type ToolCallingChatModel = model.ToolCallingChatModel

// StreamingGenerateModel wraps a ToolCallingChatModel and implements
// Generate by internally calling Stream and aggregating chunks.
//
// This works around a vLLM bug where the non-streaming /v1/chat/completions
// endpoint fails to populate tool_calls in the response for GPT-OSS models,
// even though the streaming endpoint returns them correctly.
type StreamingGenerateModel struct {
	inner model.ToolCallingChatModel
}

var _ model.ChatModel = (*StreamingGenerateModel)(nil)             //nolint:staticcheck // SA1019: ChatModel deprecated; we implement both for compatibility
var _ model.ToolCallingChatModel = (*StreamingGenerateModel)(nil)

// NewStreamingGenerateModel wraps a ToolCallingChatModel so that Generate
// calls are executed via Stream + aggregation.
func NewStreamingGenerateModel(m model.ToolCallingChatModel) *StreamingGenerateModel {
	return &StreamingGenerateModel{inner: m}
}

// Generate implements model.BaseChatModel by calling Stream and collecting
// all chunks into a single message via schema.ConcatMessages.
//
// If the context carries an *LLMTiming (via ContextWithLLMTiming), Generate
// populates TTFT and GenerationDuration so the caller can record metrics.
func (s *StreamingGenerateModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	streamStart := time.Now()
	reader, err := s.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var (
		chunks         []*schema.Message
		firstChunkTime time.Time
	)
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		if len(chunks) == 0 {
			firstChunkTime = time.Now()
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) == 0 {
		return &schema.Message{Role: schema.Assistant}, nil
	}

	lastChunkTime := time.Now()

	// Populate timing if the caller provided a LLMTiming struct.
	if timing := LLMTimingFromContext(ctx); timing != nil {
		timing.TTFT = firstChunkTime.Sub(streamStart)
		timing.GenerationDuration = lastChunkTime.Sub(firstChunkTime)
	}

	msg, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	normalizeToolCalls(msg)
	return msg, nil
}

// normalizeToolCalls fixes model output quirks before the message enters the
// ReAct graph state. Runs once per Generate call at the model-adapter boundary.
func normalizeToolCalls(msg *schema.Message) {
	for i := range msg.ToolCalls {
		// Some models omit arguments for parameterless tools → empty string.
		// The omitempty tag then drops the "arguments" key entirely, which
		// makes vLLM reject the next request with 400 "Field required".
		if msg.ToolCalls[i].Function.Arguments == "" {
			msg.ToolCalls[i].Function.Arguments = "{}"
		}
	}
}

// Stream delegates directly to the inner model.
func (s *StreamingGenerateModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return s.inner.Stream(ctx, input, opts...)
}

// BindTools implements model.ChatModel for backward compatibility.
func (s *StreamingGenerateModel) BindTools(tools []*schema.ToolInfo) error {
	wrapped, err := s.inner.WithTools(tools)
	if err != nil {
		return err
	}
	s.inner = wrapped
	return nil
}

// WithTools returns a new StreamingGenerateModel wrapping the inner model
// with the given tools bound.
func (s *StreamingGenerateModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := s.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &StreamingGenerateModel{inner: inner}, nil
}
