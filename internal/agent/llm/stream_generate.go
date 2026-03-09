// Package llm provides a factory for creating Eino ChatModel instances.
package llm

import (
	"context"
	"errors"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

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
func (s *StreamingGenerateModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	reader, err := s.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var chunks []*schema.Message
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) == 0 {
		return &schema.Message{Role: schema.Assistant}, nil
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
