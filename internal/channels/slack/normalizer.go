package slack

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jescarri/open-nipper/internal/models"
)

// SlackEnvelope is the top-level JSON sent by the Slack Events API.
type SlackEnvelope struct {
	Token       string          `json:"token"`
	TeamID      string          `json:"team_id"`
	APIAppID    string          `json:"api_app_id"`
	Type        string          `json:"type"`
	Challenge   string          `json:"challenge,omitempty"`
	Event       json.RawMessage `json:"event,omitempty"`
	EventID     string          `json:"event_id,omitempty"`
	EventTime   int64           `json:"event_time,omitempty"`
}

// SlackEvent represents the inner event payload for message events.
type SlackEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	User      string `json:"user,omitempty"`
	BotID     string `json:"bot_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Channel   string `json:"channel,omitempty"`
	TS        string `json:"ts,omitempty"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`

	// File attachments (simplified)
	Files []SlackFile `json:"files,omitempty"`
}

// SlackFile represents a Slack file attachment.
type SlackFile struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimetype,omitempty"`
	URLPrivate string `json:"url_private,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// NormalizerConfig holds settings needed by the Slack normalizer.
type NormalizerConfig struct {
	BotToken string
	AppID    string
}

// NormalizeResult holds the result of normalizing a Slack payload, including
// any challenge response that should be returned to Slack.
type NormalizeResult struct {
	Message   *models.NipperMessage
	Challenge string // non-empty for url_verification challenges
}

// NormalizeInbound converts a raw Slack Events API payload into a NipperMessage.
// Returns (nil, nil) for events that should be silently ignored.
func NormalizeInbound(raw []byte, cfg NormalizerConfig) (*models.NipperMessage, error) {
	var env SlackEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("slack normalizer: unmarshal envelope: %w", err)
	}

	// url_verification challenges are handled at the webhook layer, not here.
	if env.Type == "url_verification" {
		return nil, nil
	}

	if env.Type != "event_callback" {
		return nil, nil
	}

	if len(env.Event) == 0 {
		return nil, nil
	}

	var event SlackEvent
	if err := json.Unmarshal(env.Event, &event); err != nil {
		return nil, fmt.Errorf("slack normalizer: unmarshal event: %w", err)
	}

	if event.Type != "message" {
		return nil, nil
	}

	// Filter bot messages to prevent echo loops.
	if event.BotID != "" {
		return nil, nil
	}

	// Filter message subtypes that are not new user messages.
	if isFilteredSubtype(event.Subtype) {
		return nil, nil
	}

	if event.User == "" {
		return nil, nil
	}

	content := buildContent(&event)

	meta := models.SlackMeta{
		TeamID:      env.TeamID,
		ChannelID:   event.Channel,
		SlackUserID: event.User,
		ThreadTS:    event.ThreadTS,
		MessageTS:   event.TS,
		AppID:       env.APIAppID,
		BotToken:    cfg.BotToken,
	}

	replyMode := "inline"
	threadID := ""
	if event.ThreadTS != "" {
		replyMode = "thread"
		threadID = event.ThreadTS
	}

	ts := time.Now().UTC()
	if env.EventTime > 0 {
		ts = time.Unix(env.EventTime, 0).UTC()
	}

	nm := &models.NipperMessage{
		MessageID:       uuid.New().String(),
		OriginMessageID: event.TS,
		ChannelType:     models.ChannelTypeSlack,
		ChannelIdentity: event.User,
		Content:         content,
		Meta:            meta,
		Timestamp:       ts,
		DeliveryContext: models.DeliveryContext{
			ChannelType:  models.ChannelTypeSlack,
			ChannelID:    event.Channel,
			ThreadID:     threadID,
			ReplyMode:    replyMode,
			Capabilities: models.SlackCapabilities(),
		},
	}

	return nm, nil
}

func buildContent(event *SlackEvent) models.MessageContent {
	if len(event.Files) == 0 {
		return models.MessageContent{Text: event.Text}
	}

	parts := []models.ContentPart{}
	if event.Text != "" {
		parts = append(parts, models.ContentPart{Type: "text", Text: event.Text})
	}

	for _, f := range event.Files {
		partType := mimeToPartType(f.MimeType)
		parts = append(parts, models.ContentPart{
			Type:     partType,
			MimeType: f.MimeType,
			URL:      f.URLPrivate,
			Caption:  f.Name,
		})
	}

	text := event.Text
	if text == "" && len(event.Files) > 0 {
		text = fmt.Sprintf("[%d file(s) attached]", len(event.Files))
	}

	return models.MessageContent{
		Text:  text,
		Parts: parts,
	}
}

func mimeToPartType(mime string) string {
	switch {
	case len(mime) >= 5 && mime[:5] == "image":
		return "image"
	case len(mime) >= 5 && mime[:5] == "audio":
		return "audio"
	case len(mime) >= 5 && mime[:5] == "video":
		return "video"
	default:
		return "document"
	}
}

// isFilteredSubtype returns true for Slack event subtypes that should not
// trigger agent runs.
func isFilteredSubtype(subtype string) bool {
	switch subtype {
	case "message_changed", "message_deleted", "message_replied",
		"channel_join", "channel_leave", "channel_topic", "channel_purpose",
		"channel_name", "channel_archive", "channel_unarchive",
		"group_join", "group_leave", "group_topic", "group_purpose",
		"group_name", "group_archive", "group_unarchive",
		"file_share", "file_comment", "file_mention",
		"pinned_item", "unpinned_item",
		"bot_message", "bot_add", "bot_remove",
		"ekm_access_denied", "me_message",
		"thread_broadcast":
		return true
	}
	return false
}
