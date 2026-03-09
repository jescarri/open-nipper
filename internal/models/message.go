// Package models defines the canonical data types used throughout the Open-Nipper gateway.
package models

import (
	"encoding/json"
	"time"
)

// NipperMessage is the normalized inbound message that flows through the message pipeline.
type NipperMessage struct {
	MessageID       string         `json:"messageId"`
	OriginMessageID string         `json:"originMessageId"`
	UserID          string         `json:"userId"`
	SessionKey      string         `json:"sessionKey"`
	ChannelType     ChannelType    `json:"channelType"`
	ChannelIdentity string         `json:"channelIdentity"`
	Content         MessageContent `json:"content"`
	DeliveryContext DeliveryContext `json:"deliveryContext"`
	Meta            ChannelMeta    `json:"meta,omitempty"`
	Timestamp       time.Time      `json:"timestamp"`
	DedupeKey       string         `json:"dedupeKey,omitempty"`
}

// MessageContent holds the text and optional multimodal content parts.
type MessageContent struct {
	Text  string        `json:"text,omitempty"`
	Parts []ContentPart `json:"parts,omitempty"`
}

// ContentPart is a single element of a multimodal message.
type ContentPart struct {
	Type     string `json:"type"`            // "text" | "image" | "audio" | "video" | "document" | "location" | "contact"
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Caption  string `json:"caption,omitempty"`

	// Transcript is populated by the media enrichment pipeline for audio parts.
	// The primary prompt injection happens via msg.Content.Text mutation in the
	// pipeline; this field exists so buildMediaAnnotations can skip transcribed
	// audio parts and for logging/debugging.
	Transcript string `json:"transcript,omitempty"`

	// Location fields
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Address   string  `json:"address,omitempty"`
}

// NipperEvent is a streaming event emitted by an agent and consumed by the gateway.
type NipperEvent struct {
	EventID      string          `json:"eventId"`
	Type         NipperEventType `json:"type"`
	SessionKey   string          `json:"sessionKey"`
	ResponseID   string          `json:"responseId"`
	Timestamp    time.Time       `json:"timestamp"`
	Delta        *EventDelta     `json:"delta,omitempty"`
	ToolInfo     *EventToolInfo  `json:"toolInfo,omitempty"`
	SkillInfo    *EventSkillInfo `json:"skillInfo,omitempty"`
	Thinking     *EventThinking  `json:"thinking,omitempty"`
	Error        *EventError     `json:"error,omitempty"`
	ContextUsage *ContextUsage   `json:"contextUsage,omitempty"`
	UserID       string          `json:"userId,omitempty"`
}

// NipperEventType enumerates the types of events an agent can emit.
type NipperEventType string

const (
	EventTypeDelta               NipperEventType = "delta"
	EventTypeToolStart           NipperEventType = "tool_start"
	EventTypeToolProgress        NipperEventType = "tool_progress"
	EventTypeToolEnd             NipperEventType = "tool_end"
	EventTypeThinking            NipperEventType = "thinking"
	EventTypeError               NipperEventType = "error"
	EventTypeDone                NipperEventType = "done"
	EventTypeSkillLoaded         NipperEventType = "skill_loaded"
	EventTypeSkillExecutionStart NipperEventType = "skill_execution_start"
	EventTypeSkillExecutionEnd   NipperEventType = "skill_execution_end"
	EventTypeSkillSecretResolved NipperEventType = "skill_secret_resolved"
)

// EventDelta carries a text chunk from the model.
type EventDelta struct {
	Text string `json:"text"`
}

// EventToolInfo carries metadata about a tool invocation.
type EventToolInfo struct {
	ToolName   string `json:"toolName"`
	ToolCallID string `json:"toolCallId,omitempty"`
	Input      any    `json:"input,omitempty"`
	Output     any    `json:"output,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
}

// EventThinking carries the model's reasoning text.
type EventThinking struct {
	Text string `json:"text"`
}

// EventError carries error information from a failed agent run.
type EventError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Recoverable bool   `json:"recoverable"`
}

// EventSkillInfo carries metadata about skill lifecycle and execution (Stage 5 observability).
// Used for skill_loaded, skill_execution_start, skill_execution_end, skill_secret_resolved.
type EventSkillInfo struct {
	SkillName     string `json:"skillName,omitempty"`
	HasConfig     bool   `json:"hasConfig,omitempty"`
	HasEntrypoint bool   `json:"hasEntrypoint,omitempty"`
	Args          string `json:"args,omitempty"`          // sanitized
	ExitCode      int    `json:"exitCode,omitempty"`
	DurationMs    int64  `json:"durationMs,omitempty"`
	SecretName    string `json:"secretName,omitempty"`
	Provider      string `json:"provider,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler for NipperMessage.
// It uses channelType to pick the correct concrete ChannelMeta implementation.
func (m *NipperMessage) UnmarshalJSON(data []byte) error {
	// shadow struct: same fields but meta as raw bytes
	type shadow struct {
		MessageID       string          `json:"messageId"`
		OriginMessageID string          `json:"originMessageId"`
		UserID          string          `json:"userId"`
		SessionKey      string          `json:"sessionKey"`
		ChannelType     ChannelType     `json:"channelType"`
		ChannelIdentity string          `json:"channelIdentity"`
		Content         MessageContent  `json:"content"`
		DeliveryContext DeliveryContext `json:"deliveryContext"`
		Meta            json.RawMessage `json:"meta,omitempty"`
		Timestamp       time.Time       `json:"timestamp"`
		DedupeKey       string          `json:"dedupeKey,omitempty"`
	}

	var s shadow
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	m.MessageID = s.MessageID
	m.OriginMessageID = s.OriginMessageID
	m.UserID = s.UserID
	m.SessionKey = s.SessionKey
	m.ChannelType = s.ChannelType
	m.ChannelIdentity = s.ChannelIdentity
	m.Content = s.Content
	m.DeliveryContext = s.DeliveryContext
	m.Timestamp = s.Timestamp
	m.DedupeKey = s.DedupeKey

	if len(s.Meta) == 0 || string(s.Meta) == "null" {
		return nil
	}

	switch s.ChannelType {
	case ChannelTypeWhatsApp:
		var v WhatsAppMeta
		if err := json.Unmarshal(s.Meta, &v); err != nil {
			return err
		}
		m.Meta = v
	case ChannelTypeSlack:
		var v SlackMeta
		if err := json.Unmarshal(s.Meta, &v); err != nil {
			return err
		}
		m.Meta = v
	case ChannelTypeCron:
		var v CronMeta
		if err := json.Unmarshal(s.Meta, &v); err != nil {
			return err
		}
		m.Meta = v
	case ChannelTypeMQTT:
		var v MqttMeta
		if err := json.Unmarshal(s.Meta, &v); err != nil {
			return err
		}
		m.Meta = v
	case ChannelTypeRabbitMQ:
		var v RabbitMqMeta
		if err := json.Unmarshal(s.Meta, &v); err != nil {
			return err
		}
		m.Meta = v
	default:
		// Unknown channel — store raw bytes as RawMeta so nothing is lost.
		m.Meta = RawMeta(s.Meta)
	}
	return nil
}

// NipperResponse is the assembled outbound response delivered to the user's channel.
type NipperResponse struct {
	ResponseID      string          `json:"responseId"`
	SessionKey      string          `json:"sessionKey"`
	UserID          string          `json:"userId"`
	ChannelType     ChannelType     `json:"channelType"`
	ChannelIdentity string          `json:"channelIdentity"`
	Text            string          `json:"text"`
	Parts           []ContentPart   `json:"parts,omitempty"`
	DeliveryContext DeliveryContext  `json:"deliveryContext"`
	Meta            ChannelMeta     `json:"meta,omitempty"`
	OriginMessageID string          `json:"originMessageId,omitempty"`
	Timestamp       time.Time       `json:"timestamp"`
	ContextUsage    *ContextUsage   `json:"contextUsage,omitempty"`
}
