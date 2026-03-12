package whatsapp

import (
	"encoding/json"
	"testing"

	"github.com/jescarri/open-nipper/internal/models"
)

var testCfg = NormalizerConfig{
	WuzapiBaseURL:      "http://localhost:8080",
	WuzapiInstanceName: "nipper-wa",
}

func TestNormalizeInbound_TextConversation(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"3EB06F9067F80BAB89FF",
			"MessageSource":{
				"Chat":"1555010002@s.whatsapp.net",
				"Sender":"1555010001@s.whatsapp.net",
				"IsFromMe":false,
				"IsGroup":false
			},
			"PushName":"Alice",
			"Timestamp":"2026-02-21T10:00:05Z"
		},
		"Message":{"Conversation":"Deploy the payments service to staging"},
		"userID":"1",
		"instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	if msg.ChannelType != models.ChannelTypeWhatsApp {
		t.Fatalf("expected channelType whatsapp, got %s", msg.ChannelType)
	}
	if msg.Content.Text != "Deploy the payments service to staging" {
		t.Fatalf("unexpected text: %q", msg.Content.Text)
	}
	if msg.OriginMessageID != "3EB06F9067F80BAB89FF" {
		t.Fatalf("unexpected origin message ID: %s", msg.OriginMessageID)
	}
	if msg.ChannelIdentity != "1555010001@s.whatsapp.net" {
		t.Fatalf("unexpected channel identity: %s", msg.ChannelIdentity)
	}
	if msg.DeliveryContext.ChannelID != "1555010002@s.whatsapp.net" {
		t.Fatalf("unexpected delivery channel ID: %s", msg.DeliveryContext.ChannelID)
	}

	meta, ok := msg.Meta.(models.WhatsAppMeta)
	if !ok {
		t.Fatal("meta should be WhatsAppMeta")
	}
	if meta.PushName != "Alice" {
		t.Fatalf("unexpected push name: %s", meta.PushName)
	}
	if meta.IsGroup {
		t.Fatal("should not be a group message")
	}
	if meta.WuzapiBaseURL != "http://localhost:8080" {
		t.Fatalf("unexpected wuzapi base url: %s", meta.WuzapiBaseURL)
	}
}

func TestNormalizeInbound_IsFromMe_Filtered(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-self-001",
			"MessageSource":{
				"Chat":"1555010002@s.whatsapp.net",
				"Sender":"1555010002@s.whatsapp.net",
				"IsFromMe":true,
				"IsGroup":false
			},
			"PushName":"Me"
		},
		"Message":{"Conversation":"my own message"},
		"userID":"1",
		"instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("self-messages should return nil")
	}
}

func TestNormalizeInbound_NonMessageEvent_Filtered(t *testing.T) {
	raw := `{"type":"ReadReceipt","Info":{"ID":"wa-001"},"Message":{},"userID":"1"}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("non-Message events should return nil")
	}
}

func TestNormalizeInbound_ImageMessageWithS3(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-img-001",
			"MessageSource":{
				"Chat":"1555010002@s.whatsapp.net",
				"Sender":"1555010001@s.whatsapp.net",
				"IsFromMe":false,
				"IsGroup":false
			},
			"PushName":"Alice"
		},
		"Message":{
			"ImageMessage":{
				"Caption":"What error is this?",
				"Mimetype":"image/jpeg",
				"FileLength":245632,
				"URL":"https://mmg.whatsapp.net/...",
				"MediaKey":"base64key..."
			}
		},
		"s3":{
			"url":"https://my-bucket.s3.amazonaws.com/image.jpg",
			"mimeType":"image/jpeg",
			"fileName":"wa-img-001.jpg",
			"size":245632
		},
		"userID":"1",
		"instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	if msg.Content.Text != "What error is this?" {
		t.Fatalf("expected caption as text, got %q", msg.Content.Text)
	}
	if len(msg.Content.Parts) < 1 {
		t.Fatal("expected content parts for image")
	}

	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if !meta.HasMedia {
		t.Fatal("expected hasMedia true")
	}
	if meta.MediaType != "image" {
		t.Fatalf("expected mediaType image, got %s", meta.MediaType)
	}
	if meta.S3URL != "https://my-bucket.s3.amazonaws.com/image.jpg" {
		t.Fatalf("expected s3 url, got %s", meta.S3URL)
	}
}

func TestNormalizeInbound_ImageMessageWithoutCaption(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-img-002",
			"MessageSource":{"Chat":"chat@s.whatsapp.net","Sender":"sender@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Bob"
		},
		"Message":{"ImageMessage":{"Mimetype":"image/png","FileLength":1024}},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content.Text != "[image message]" {
		t.Fatalf("expected placeholder text, got %q", msg.Content.Text)
	}
}

func TestNormalizeInbound_AudioMessage(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-aud-001",
			"MessageSource":{"Chat":"chat@s.whatsapp.net","Sender":"sender@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Alice"
		},
		"Message":{"AudioMessage":{"Mimetype":"audio/ogg","FileLength":8000}},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil")
	}

	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if meta.MediaType != "audio" {
		t.Fatalf("expected audio, got %s", meta.MediaType)
	}
}

func TestNormalizeInbound_VideoMessage(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-vid-001",
			"MessageSource":{"Chat":"chat@s.whatsapp.net","Sender":"sender@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Alice"
		},
		"Message":{"VideoMessage":{"Mimetype":"video/mp4","FileLength":5000000,"Caption":"Check this"}},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if meta.MediaType != "video" {
		t.Fatalf("expected video, got %s", meta.MediaType)
	}
	if msg.Content.Text != "Check this" {
		t.Fatalf("expected caption, got %q", msg.Content.Text)
	}
}

func TestNormalizeInbound_DocumentMessage(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-doc-001",
			"MessageSource":{"Chat":"chat@s.whatsapp.net","Sender":"sender@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Alice"
		},
		"Message":{"DocumentMessage":{"Mimetype":"application/pdf","FileLength":12345,"FileName":"report.pdf","Caption":"Monthly report"}},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if meta.MediaType != "document" {
		t.Fatalf("expected document, got %s", meta.MediaType)
	}
}

func TestNormalizeInbound_LocationMessage(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-loc-001",
			"MessageSource":{"Chat":"chat@s.whatsapp.net","Sender":"sender@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Alice"
		},
		"Message":{"LocationMessage":{"DegreesLatitude":-34.603722,"DegreesLongitude":-58.381592,"Name":"Buenos Aires"}},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.Content.Parts) != 1 || msg.Content.Parts[0].Type != "location" {
		t.Fatal("expected location content part")
	}
	if msg.Content.Parts[0].Latitude == 0 {
		t.Fatal("expected non-zero latitude")
	}
}

func TestNormalizeInbound_ContactMessage(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-contact-001",
			"MessageSource":{"Chat":"chat@s.whatsapp.net","Sender":"sender@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Alice"
		},
		"Message":{"ContactMessage":{"DisplayName":"John Doe","Vcard":"BEGIN:VCARD\nFN:John Doe\nEND:VCARD"}},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content.Text != "John Doe" {
		t.Fatalf("expected display name, got %q", msg.Content.Text)
	}
	if len(msg.Content.Parts) != 1 || msg.Content.Parts[0].Type != "contact" {
		t.Fatal("expected contact content part")
	}
}

func TestNormalizeInbound_ExtendedTextWithQuote(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-ext-001",
			"MessageSource":{"Chat":"chat@s.whatsapp.net","Sender":"sender@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Alice"
		},
		"Message":{
			"ExtendedTextMessage":{
				"Text":"Replying to your question",
				"ContextInfo":{
					"StanzaId":"quoted-msg-001",
					"Participant":"other@s.whatsapp.net"
				}
			}
		},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content.Text != "Replying to your question" {
		t.Fatalf("unexpected text: %q", msg.Content.Text)
	}

	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if meta.QuotedMessageID != "quoted-msg-001" {
		t.Fatalf("expected quoted message ID, got %q", meta.QuotedMessageID)
	}
	if meta.QuotedParticipant != "other@s.whatsapp.net" {
		t.Fatalf("expected quoted participant, got %q", meta.QuotedParticipant)
	}
}

func TestNormalizeInbound_GroupMessage(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-grp-001",
			"MessageSource":{
				"Chat":"12025550100@g.us",
				"Sender":"1555010001@s.whatsapp.net",
				"IsFromMe":false,
				"IsGroup":true
			},
			"PushName":"Alice"
		},
		"Message":{"Conversation":"Hello group"},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if !meta.IsGroup {
		t.Fatal("expected group message")
	}
	if msg.DeliveryContext.ChannelID != "12025550100@g.us" {
		t.Fatalf("delivery channel should be group JID, got %s", msg.DeliveryContext.ChannelID)
	}
	if msg.ChannelIdentity != "1555010001@s.whatsapp.net" {
		t.Fatalf("identity should be sender JID, got %s", msg.ChannelIdentity)
	}
}

// TestNormalizeInbound_JSONWebhookMode uses a payload captured from a real
// Wuzapi instance running with WEBHOOK_FORMAT=json. The event is nested
// inside "event" and Info fields are flat (no MessageSource wrapper).
func TestNormalizeInbound_JSONWebhookMode(t *testing.T) {
	raw := `{
		"event":{
			"Info":{
				"Chat":"1555010004@lid",
				"Sender":"1555010004:26@lid",
				"IsFromMe":false,
				"IsGroup":false,
				"AddressingMode":"",
				"SenderAlt":"1555010003:26@s.whatsapp.net",
				"RecipientAlt":"",
				"ID":"3EB0C266F0FFF9567C6618",
				"Type":"text",
				"PushName":"jesus carrillo",
				"Timestamp":"2026-02-22T01:40:49-08:00"
			},
			"Message":{
				"conversation":"toing",
				"messageContextInfo":{
					"deviceListMetadata":{},
					"deviceListMetadataVersion":2
				}
			},
			"IsEphemeral":false,
			"IsViewOnce":false
		},
		"type":"Message",
		"instanceName":"Mr Robot",
		"userID":"c56dc4bab4a0809959698fa8b93d490a"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	if msg.Content.Text != "toing" {
		t.Fatalf("expected text 'toing', got %q", msg.Content.Text)
	}
	if msg.OriginMessageID != "3EB0C266F0FFF9567C6618" {
		t.Fatalf("unexpected origin message ID: %s", msg.OriginMessageID)
	}

	meta, ok := msg.Meta.(models.WhatsAppMeta)
	if !ok {
		t.Fatal("meta should be WhatsAppMeta")
	}
	// SenderAlt should be preferred over the @lid Sender
	if meta.SenderJID != "1555010003@s.whatsapp.net" {
		t.Fatalf("expected SenderAlt JID, got %q", meta.SenderJID)
	}
	if meta.PushName != "jesus carrillo" {
		t.Fatalf("unexpected push name: %q", meta.PushName)
	}
	if meta.WuzapiUserID != "c56dc4bab4a0809959698fa8b93d490a" {
		t.Fatalf("unexpected userID: %q", meta.WuzapiUserID)
	}
	if meta.WuzapiInstanceName != "Mr Robot" {
		t.Fatalf("unexpected instanceName: %q", meta.WuzapiInstanceName)
	}
	if meta.IsGroup {
		t.Fatal("should not be a group message")
	}
	if msg.ChannelIdentity != "1555010003@s.whatsapp.net" {
		t.Fatalf("channel identity should use SenderAlt, got %q", msg.ChannelIdentity)
	}
	if msg.DeliveryContext.ChannelID != "1555010004@lid" {
		t.Fatalf("delivery channel ID should be Chat JID, got %q", msg.DeliveryContext.ChannelID)
	}
}

func TestNormalizeInbound_JSONWebhookMode_IsFromMe_Filtered(t *testing.T) {
	raw := `{
		"event":{
			"Info":{
				"Chat":"1555010004@lid",
				"Sender":"1555010004:26@lid",
				"IsFromMe":true,
				"IsGroup":false,
				"ID":"self-msg-001",
				"PushName":"Me"
			},
			"Message":{"conversation":"my own message"}
		},
		"type":"Message",
		"userID":"1","instanceName":"test"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg != nil {
		t.Fatal("self-messages in JSON mode should be filtered")
	}
}

func TestNormalizeInbound_JSONWebhookMode_WithS3(t *testing.T) {
	raw := `{
		"event":{
			"Info":{
				"Chat":"1555010002@s.whatsapp.net",
				"Sender":"1555010001@s.whatsapp.net",
				"IsFromMe":false,
				"IsGroup":false,
				"ID":"wa-img-json-001",
				"PushName":"Alice"
			},
			"Message":{
				"ImageMessage":{
					"Caption":"screenshot",
					"Mimetype":"image/jpeg",
					"FileLength":245632
				}
			},
			"s3":{
				"url":"https://bucket.s3.amazonaws.com/image.jpg",
				"mimeType":"image/jpeg",
				"fileName":"wa-img-json-001.jpg",
				"size":245632
			}
		},
		"type":"Message",
		"userID":"1","instanceName":"test"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}

	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if !meta.HasMedia || meta.MediaType != "image" {
		t.Fatalf("expected image media, got hasMedia=%v mediaType=%q", meta.HasMedia, meta.MediaType)
	}
	if meta.S3URL != "https://bucket.s3.amazonaws.com/image.jpg" {
		t.Fatalf("unexpected S3 URL: %q", meta.S3URL)
	}
	if msg.Content.Text != "screenshot" {
		t.Fatalf("expected caption as text, got %q", msg.Content.Text)
	}
}

func TestNormalizeJID(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"1555010003:26@s.whatsapp.net", "1555010003@s.whatsapp.net"},
		{"1555010001@s.whatsapp.net", "1555010001@s.whatsapp.net"},
		{"1555010004:26@lid", "1555010004@lid"},
		{"12025550100@g.us", "12025550100@g.us"},
		{"nojid", "nojid"},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizeJID(tt.input)
		if got != tt.want {
			t.Errorf("normalizeJID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeInbound_InvalidJSON(t *testing.T) {
	_, err := NormalizeInbound([]byte("not json"), testCfg)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNormalizeInbound_InvalidMessageJSON(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-bad-001",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"s@s.whatsapp.net","IsFromMe":false,"IsGroup":false}
		},
		"Message":"not an object",
		"userID":"1","instanceName":"nipper-wa"
	}`

	_, err := NormalizeInbound([]byte(raw), testCfg)
	if err == nil {
		t.Fatal("expected error for non-object Message field")
	}
}

func TestNormalizeInbound_MessageIDIsUUID(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-uuid-001",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"s@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"PushName":"Bob"
		},
		"Message":{"Conversation":"hi"},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.MessageID == "" {
		t.Fatal("message ID should be set")
	}
	if msg.MessageID == msg.OriginMessageID {
		t.Fatal("gateway message ID should differ from origin message ID")
	}
}

func TestNormalizeInbound_DeliveryContextCapabilities(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-cap-001",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"s@s.whatsapp.net","IsFromMe":false,"IsGroup":false}
		},
		"Message":{"Conversation":"caps test"},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	caps := msg.DeliveryContext.Capabilities
	if caps.SupportsStreaming {
		t.Fatal("WhatsApp should not support streaming")
	}
	if !caps.SupportsImages {
		t.Fatal("WhatsApp should support images")
	}
	if !caps.SupportsDocuments {
		t.Fatal("WhatsApp should support documents")
	}
}

func TestNormalizeInbound_TimestampParsing(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-ts-001",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"s@s.whatsapp.net","IsFromMe":false,"IsGroup":false},
			"Timestamp":"2026-02-21T10:00:05Z"
		},
		"Message":{"Conversation":"time test"},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Timestamp.Year() != 2026 || msg.Timestamp.Month() != 2 {
		t.Fatalf("unexpected timestamp: %v", msg.Timestamp)
	}
}

func TestNormalizeInbound_ResultIsSerialisable(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-ser-001",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"s@s.whatsapp.net","IsFromMe":false,"IsGroup":false}
		},
		"Message":{"Conversation":"serialise me"},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("NipperMessage should be JSON-serialisable: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("serialised data should not be empty")
	}
}

func TestNormalizeInbound_StickerMessage(t *testing.T) {
	raw := `{
		"type":"Message",
		"Info":{
			"ID":"wa-stk-001",
			"MessageSource":{"Chat":"c@s.whatsapp.net","Sender":"s@s.whatsapp.net","IsFromMe":false,"IsGroup":false}
		},
		"Message":{"StickerMessage":{"Mimetype":"image/webp","FileLength":5000}},
		"userID":"1","instanceName":"nipper-wa"
	}`

	msg, err := NormalizeInbound([]byte(raw), testCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	meta, _ := msg.Meta.(models.WhatsAppMeta)
	if meta.MediaType != "sticker" {
		t.Fatalf("expected sticker, got %s", meta.MediaType)
	}
}
