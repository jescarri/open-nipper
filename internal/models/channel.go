package models

// ChannelType identifies which channel an inbound message arrived on.
type ChannelType string

const (
	ChannelTypeWhatsApp  ChannelType = "whatsapp"
	ChannelTypeSlack     ChannelType = "slack"
	ChannelTypeCron      ChannelType = "cron"
	ChannelTypeMQTT      ChannelType = "mqtt"
	ChannelTypeRabbitMQ  ChannelType = "rabbitmq"
	ChannelTypeWebSocket ChannelType = "websocket"
)

// ChannelMeta is a marker interface for channel-specific metadata.
type ChannelMeta interface {
	channelMeta()
}

// RawMeta holds unparsed channel metadata for unrecognised channel types.
// It implements ChannelMeta so unknown channels don't drop the payload.
type RawMeta []byte

func (RawMeta) channelMeta() {}

func (r RawMeta) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

// DeliveryContext describes how a response should be delivered back to the user.
type DeliveryContext struct {
	ChannelType    ChannelType          `json:"channelType"`
	ChannelID      string               `json:"channelId"`
	ReplyMode      string               `json:"replyMode"`           // "direct" | "thread" | "broadcast" | "dm" | "silent" | "escalate"
	ThreadID       string               `json:"threadId,omitempty"`
	NotifyChannels []string             `json:"notifyChannels,omitempty"`
	Capabilities   ChannelCapabilities  `json:"capabilities"`
}

// ChannelCapabilities describes what a channel can render.
type ChannelCapabilities struct {
	SupportsMarkdown     bool `json:"supportsMarkdown"`
	SupportsStreaming     bool `json:"supportsStreaming"`
	SupportsImages       bool `json:"supportsImages"`
	SupportsDocuments    bool `json:"supportsDocuments"`
	SupportsAudio        bool `json:"supportsAudio"`
	SupportsReactions    bool `json:"supportsReactions"`
	SupportsThreads      bool `json:"supportsThreads"`
	SupportsMessageEdits bool `json:"supportsMessageEdits"`
}

// WhatsAppCapabilities returns the capability matrix for WhatsApp.
func WhatsAppCapabilities() ChannelCapabilities {
	return ChannelCapabilities{
		SupportsMarkdown:     false,
		SupportsStreaming:    false,
		SupportsImages:       true,
		SupportsDocuments:    true,
		SupportsAudio:        true,
		SupportsReactions:    false,
		SupportsThreads:      false,
		SupportsMessageEdits: false,
	}
}

// SlackCapabilities returns the capability matrix for Slack.
func SlackCapabilities() ChannelCapabilities {
	return ChannelCapabilities{
		SupportsMarkdown:     true,
		SupportsStreaming:    true,
		SupportsImages:       true,
		SupportsDocuments:    true,
		SupportsAudio:        false,
		SupportsReactions:    true,
		SupportsThreads:      true,
		SupportsMessageEdits: true,
	}
}

// CronCapabilities returns the capability matrix for Cron (headless channel).
func CronCapabilities() ChannelCapabilities {
	return ChannelCapabilities{
		SupportsMarkdown:     false,
		SupportsStreaming:    false,
		SupportsImages:       false,
		SupportsDocuments:    false,
		SupportsAudio:        false,
		SupportsReactions:    false,
		SupportsThreads:      false,
		SupportsMessageEdits: false,
	}
}

// MQTTCapabilities returns the capability matrix for MQTT.
func MQTTCapabilities() ChannelCapabilities {
	return ChannelCapabilities{
		SupportsMarkdown:     false,
		SupportsStreaming:    false,
		SupportsImages:       false,
		SupportsDocuments:    false,
		SupportsAudio:        false,
		SupportsReactions:    false,
		SupportsThreads:      false,
		SupportsMessageEdits: false,
	}
}

// RabbitMQCapabilities returns the capability matrix for the RabbitMQ channel.
func RabbitMQCapabilities() ChannelCapabilities {
	return ChannelCapabilities{
		SupportsMarkdown:     false,
		SupportsStreaming:    false,
		SupportsImages:       false,
		SupportsDocuments:    false,
		SupportsAudio:        false,
		SupportsReactions:    false,
		SupportsThreads:      false,
		SupportsMessageEdits: false,
	}
}

// WhatsAppMeta holds WhatsApp-specific message metadata from the Wuzapi webhook.
type WhatsAppMeta struct {
	// Wuzapi instance
	WuzapiUserID       string `json:"wuzapiUserId"`
	WuzapiInstanceName string `json:"wuzapiInstanceName"`
	WuzapiBaseURL      string `json:"wuzapiBaseUrl"`

	// WhatsApp identifiers
	ChatJID   string `json:"chatJid"`
	SenderJID string `json:"senderJid"`
	MessageID string `json:"messageId"`
	PushName  string `json:"pushName,omitempty"`

	// Context
	IsGroup    bool `json:"isGroup"`
	IsFromMe   bool `json:"isFromMe"`

	// Quoting
	QuotedMessageID   string `json:"quotedMessageId,omitempty"`
	QuotedParticipant string `json:"quotedParticipant,omitempty"`

	// Media
	HasMedia  bool   `json:"hasMedia"`
	MediaType string `json:"mediaType,omitempty"` // "image" | "audio" | "video" | "document" | "sticker"
	S3URL     string `json:"s3Url,omitempty"`
	MediaKey  string `json:"mediaKey,omitempty"`
}

func (WhatsAppMeta) channelMeta() {}

// SlackMeta holds Slack-specific message metadata.
type SlackMeta struct {
	TeamID      string `json:"teamId,omitempty"`
	ChannelID   string `json:"channelId"`
	SlackUserID string `json:"slackUserId"`
	ThreadTS    string `json:"threadTs,omitempty"`
	MessageTS   string `json:"messageTs,omitempty"`
	AppID       string `json:"appId,omitempty"`
	BotID       string `json:"botId,omitempty"`
	BotToken    string `json:"-"` // never serialised
}

func (SlackMeta) channelMeta() {}

// CronMeta holds cron scheduler-specific message metadata.
type CronMeta struct {
	JobID      string `json:"jobId"`
	ScheduleAt string `json:"scheduleAt"`
}

func (CronMeta) channelMeta() {}

// MqttMeta holds MQTT-specific message metadata.
type MqttMeta struct {
	Broker          string `json:"broker"`
	Topic           string `json:"topic"`
	QoS             int    `json:"qos"`
	Retain          bool   `json:"retain"`
	ClientID        string `json:"clientId"`
	CorrelationID   string `json:"correlationId,omitempty"`
	ResponseTopic   string `json:"responseTopic,omitempty"`
}

func (MqttMeta) channelMeta() {}

// RabbitMqMeta holds RabbitMQ channel-specific message metadata.
type RabbitMqMeta struct {
	Exchange      string `json:"exchange"`
	RoutingKey    string `json:"routingKey"`
	CorrelationID string `json:"correlationId,omitempty"`
	ReplyTo       string `json:"replyTo,omitempty"`
	MessageID     string `json:"messageId,omitempty"`
}

func (RabbitMqMeta) channelMeta() {}
