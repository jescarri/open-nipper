package slack

import (
	"encoding/json"
	"testing"

	"github.com/open-nipper/open-nipper/internal/models"
)

func TestNormalizeInbound_TextMessage(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"team_id": "T0123",
		"api_app_id": "A0789",
		"event": {
			"type": "message",
			"user": "U0123ABC",
			"text": "deploy to staging",
			"channel": "C0456DEF",
			"ts": "1708512000.000100"
		},
		"event_time": 1708512000
	}`
	cfg := NormalizerConfig{BotToken: "xoxb-test", AppID: "A0789"}

	msg, err := NormalizeInbound([]byte(raw), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	if msg.ChannelType != models.ChannelTypeSlack {
		t.Errorf("channelType = %q, want %q", msg.ChannelType, models.ChannelTypeSlack)
	}
	if msg.Content.Text != "deploy to staging" {
		t.Errorf("text = %q, want %q", msg.Content.Text, "deploy to staging")
	}
	if msg.ChannelIdentity != "U0123ABC" {
		t.Errorf("channelIdentity = %q, want %q", msg.ChannelIdentity, "U0123ABC")
	}
	if msg.OriginMessageID != "1708512000.000100" {
		t.Errorf("originMessageID = %q, want %q", msg.OriginMessageID, "1708512000.000100")
	}
	if msg.DeliveryContext.ChannelID != "C0456DEF" {
		t.Errorf("deliveryContext.channelID = %q, want %q", msg.DeliveryContext.ChannelID, "C0456DEF")
	}
	if msg.DeliveryContext.ReplyMode != "inline" {
		t.Errorf("replyMode = %q, want %q", msg.DeliveryContext.ReplyMode, "inline")
	}
}

func TestNormalizeInbound_ThreadedMessage(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"team_id": "T0123",
		"event": {
			"type": "message",
			"user": "U0123ABC",
			"text": "follow up",
			"channel": "C0456DEF",
			"ts": "1708512100.000200",
			"thread_ts": "1708511900.000050"
		},
		"event_time": 1708512100
	}`

	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	if msg.DeliveryContext.ReplyMode != "thread" {
		t.Errorf("replyMode = %q, want %q", msg.DeliveryContext.ReplyMode, "thread")
	}
	if msg.DeliveryContext.ThreadID != "1708511900.000050" {
		t.Errorf("threadID = %q, want %q", msg.DeliveryContext.ThreadID, "1708511900.000050")
	}

	meta, ok := msg.Meta.(models.SlackMeta)
	if !ok {
		t.Fatal("meta is not SlackMeta")
	}
	if meta.ThreadTS != "1708511900.000050" {
		t.Errorf("threadTS = %q, want %q", meta.ThreadTS, "1708511900.000050")
	}
}

func TestNormalizeInbound_BotMessageFiltered(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"bot_id": "B0123",
			"text": "automated message",
			"channel": "C0456DEF",
			"ts": "1708512000.000100"
		}
	}`

	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil (bot message should be filtered)")
	}
}

func TestNormalizeInbound_MessageChangedFiltered(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"subtype": "message_changed",
			"user": "U0123ABC",
			"text": "edited message",
			"channel": "C0456DEF",
			"ts": "1708512000.000100"
		}
	}`

	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil (message_changed should be filtered)")
	}
}

func TestNormalizeInbound_URLVerification(t *testing.T) {
	raw := `{
		"type": "url_verification",
		"challenge": "abc123xyz"
	}`

	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for url_verification")
	}
}

func TestNormalizeInbound_NonMessageEvent(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "reaction_added",
			"user": "U0123ABC"
		}
	}`

	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for non-message event")
	}
}

func TestNormalizeInbound_InvalidJSON(t *testing.T) {
	raw := `{not valid json`
	_, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNormalizeInbound_EmptyUser(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"text": "no user field",
			"channel": "C0456DEF",
			"ts": "1708512000.000100"
		}
	}`

	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil (empty user should be filtered)")
	}
}

func TestNormalizeInbound_SlackMeta(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"team_id": "T999",
		"api_app_id": "ATEST",
		"event": {
			"type": "message",
			"user": "UABC",
			"text": "hello",
			"channel": "CDEF",
			"ts": "123.456"
		},
		"event_time": 1708512000
	}`
	cfg := NormalizerConfig{BotToken: "xoxb-test-token", AppID: "ATEST"}

	msg, err := NormalizeInbound([]byte(raw), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, ok := msg.Meta.(models.SlackMeta)
	if !ok {
		t.Fatal("meta is not SlackMeta")
	}
	if meta.TeamID != "T999" {
		t.Errorf("teamID = %q, want %q", meta.TeamID, "T999")
	}
	if meta.ChannelID != "CDEF" {
		t.Errorf("channelID = %q, want %q", meta.ChannelID, "CDEF")
	}
	if meta.SlackUserID != "UABC" {
		t.Errorf("slackUserID = %q, want %q", meta.SlackUserID, "UABC")
	}
	if meta.MessageTS != "123.456" {
		t.Errorf("messageTS = %q, want %q", meta.MessageTS, "123.456")
	}
	if meta.AppID != "ATEST" {
		t.Errorf("appID = %q, want %q", meta.AppID, "ATEST")
	}
	if meta.BotToken != "xoxb-test-token" {
		t.Errorf("botToken = %q, want %q", meta.BotToken, "xoxb-test-token")
	}
}

func TestNormalizeInbound_DeliveryContextCapabilities(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"user": "U001",
			"text": "test",
			"channel": "C001",
			"ts": "100.001"
		}
	}`
	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	caps := msg.DeliveryContext.Capabilities
	if !caps.SupportsStreaming {
		t.Error("slack should support streaming")
	}
	if !caps.SupportsMarkdown {
		t.Error("slack should support markdown")
	}
	if !caps.SupportsThreads {
		t.Error("slack should support threads")
	}
	if !caps.SupportsMessageEdits {
		t.Error("slack should support message edits")
	}
	if !caps.SupportsReactions {
		t.Error("slack should support reactions")
	}
}

func TestNormalizeInbound_FileAttachment(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"user": "U001",
			"text": "check this image",
			"channel": "C001",
			"ts": "100.001",
			"files": [
				{
					"id": "F001",
					"name": "screenshot.png",
					"mimetype": "image/png",
					"url_private": "https://files.slack.com/F001/screenshot.png",
					"size": 12345
				}
			]
		}
	}`
	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Content.Text != "check this image" {
		t.Errorf("text = %q, want %q", msg.Content.Text, "check this image")
	}
	if len(msg.Content.Parts) != 2 {
		t.Fatalf("expected 2 parts (text + image), got %d", len(msg.Content.Parts))
	}
	if msg.Content.Parts[1].Type != "image" {
		t.Errorf("part[1].type = %q, want %q", msg.Content.Parts[1].Type, "image")
	}
	if msg.Content.Parts[1].URL != "https://files.slack.com/F001/screenshot.png" {
		t.Errorf("part[1].url = %q", msg.Content.Parts[1].URL)
	}
}

func TestNormalizeInbound_UUIDGeneration(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"user": "U001",
			"text": "hello",
			"channel": "C001",
			"ts": "100.001"
		}
	}`
	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.MessageID == "" {
		t.Error("expected non-empty messageId")
	}
}

func TestNormalizeInbound_Serialisable(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"user": "U001",
			"text": "hello",
			"channel": "C001",
			"ts": "100.001"
		}
	}`
	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := json.Marshal(msg); err != nil {
		t.Fatalf("failed to marshal NipperMessage: %v", err)
	}
}

func TestNormalizeInbound_ChannelJoinFiltered(t *testing.T) {
	raw := `{
		"type": "event_callback",
		"event": {
			"type": "message",
			"subtype": "channel_join",
			"user": "U001",
			"text": "joined",
			"channel": "C001",
			"ts": "100.001"
		}
	}`
	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil (channel_join should be filtered)")
	}
}

func TestNormalizeInbound_NonEventCallback(t *testing.T) {
	raw := `{"type": "app_rate_limited", "token": "test"}`
	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for app_rate_limited event")
	}
}

func TestNormalizeInbound_EmptyEvent(t *testing.T) {
	raw := `{"type": "event_callback"}`
	msg, err := NormalizeInbound([]byte(raw), NormalizerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil for empty event field")
	}
}

func TestFilteredSubtypes(t *testing.T) {
	cases := []string{
		"message_changed", "message_deleted", "message_replied",
		"channel_join", "channel_leave", "bot_message",
		"pinned_item", "unpinned_item", "thread_broadcast",
	}
	for _, sub := range cases {
		if !isFilteredSubtype(sub) {
			t.Errorf("expected %q to be filtered", sub)
		}
	}
	if isFilteredSubtype("") {
		t.Error("empty subtype should not be filtered")
	}
	if isFilteredSubtype("some_unknown") {
		t.Error("unknown subtype should not be filtered")
	}
}
