package whatsapp

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jescarri/open-nipper/internal/models"
)

// WuzapiWebhook is the top-level JSON structure sent by Wuzapi webhook callbacks.
// In JSON webhook mode the event payload is nested under an "event" key;
// in form mode the fields appear at the top level. Both layouts are supported.
type WuzapiWebhook struct {
	Type         string          `json:"type"`
	Event        *WuzapiEvent    `json:"event,omitempty"`
	Info         WuzapiInfo      `json:"Info"`
	Message      json.RawMessage `json:"Message"`
	S3           *WuzapiS3       `json:"s3,omitempty"`
	UserID       string          `json:"userID"`
	InstanceName string          `json:"instanceName"`
	Token        string          `json:"token,omitempty"`
}

// WuzapiEvent is the nested event object sent in JSON webhook mode.
type WuzapiEvent struct {
	Info    WuzapiInfo      `json:"Info"`
	Message json.RawMessage `json:"Message"`
	S3      *WuzapiS3       `json:"s3,omitempty"`
}

// WuzapiInfo holds envelope metadata from a Wuzapi webhook event.
// whatsmeow embeds MessageSource so Chat/Sender/IsFromMe/IsGroup appear as
// flat top-level fields. We accept both the flat layout and a nested
// "MessageSource" object for backward compatibility.
type WuzapiInfo struct {
	ID            string              `json:"ID"`
	MessageSource WuzapiMessageSource `json:"MessageSource"`
	// Flat fields produced by whatsmeow's embedded MessageSource.
	Chat     string `json:"Chat"`
	Sender   string `json:"Sender"`
	IsFromMe bool   `json:"IsFromMe"`
	IsGroup  bool   `json:"IsGroup"`
	// SenderAlt / RecipientAlt carry the @s.whatsapp.net JID when the
	// primary Sender uses the newer @lid addressing.
	SenderAlt string `json:"SenderAlt"`
	PushName  string `json:"PushName"`
	Timestamp string `json:"Timestamp"`
	Type      string `json:"Type"`
}

// ResolvedChat returns the best available chat JID, preferring the nested
// MessageSource value, then the flat field.
func (i *WuzapiInfo) ResolvedChat() string {
	if i.MessageSource.Chat != "" {
		return i.MessageSource.Chat
	}
	return i.Chat
}

// ResolvedSender returns the best available sender JID.
// It prefers SenderAlt (@s.whatsapp.net) over @lid addresses, then falls back
// to MessageSource.Sender and the flat Sender field.
func (i *WuzapiInfo) ResolvedSender() string {
	if i.SenderAlt != "" {
		return i.SenderAlt
	}
	if i.MessageSource.Sender != "" {
		return i.MessageSource.Sender
	}
	return i.Sender
}

// ResolvedIsFromMe returns the IsFromMe flag from whichever source has data.
func (i *WuzapiInfo) ResolvedIsFromMe() bool {
	if i.MessageSource.Chat != "" {
		return i.MessageSource.IsFromMe
	}
	return i.IsFromMe
}

// ResolvedIsGroup returns the IsGroup flag from whichever source has data.
func (i *WuzapiInfo) ResolvedIsGroup() bool {
	if i.MessageSource.Chat != "" {
		return i.MessageSource.IsGroup
	}
	return i.IsGroup
}

// WuzapiMessageSource identifies the chat and sender (nested format).
type WuzapiMessageSource struct {
	Chat     string `json:"Chat"`
	Sender   string `json:"Sender"`
	IsFromMe bool   `json:"IsFromMe"`
	IsGroup  bool   `json:"IsGroup"`
}

// WuzapiS3 holds optional S3 storage info for media messages.
type WuzapiS3 struct {
	URL      string `json:"url"`
	MimeType string `json:"mimeType"`
	FileName string `json:"fileName"`
	Size     int64  `json:"size"`
}

// wuzapiMessage is a partial struct for parsing the polymorphic Message field.
type wuzapiMessage struct {
	Conversation    string               `json:"Conversation,omitempty"`
	ExtendedText    *wuzapiExtendedText  `json:"ExtendedTextMessage,omitempty"`
	ImageMessage    *wuzapiMediaMessage  `json:"ImageMessage,omitempty"`
	AudioMessage    *wuzapiMediaMessage  `json:"AudioMessage,omitempty"`
	VideoMessage    *wuzapiMediaMessage  `json:"VideoMessage,omitempty"`
	DocumentMessage *wuzapiMediaMessage  `json:"DocumentMessage,omitempty"`
	StickerMessage  *wuzapiMediaMessage  `json:"StickerMessage,omitempty"`
	LocationMessage *wuzapiLocation      `json:"LocationMessage,omitempty"`
	ContactMessage  *wuzapiContact       `json:"ContactMessage,omitempty"`
}

type wuzapiExtendedText struct {
	Text        string           `json:"Text"`
	ContextInfo *wuzapiContextInfo `json:"ContextInfo,omitempty"`
}

type wuzapiMediaMessage struct {
	Caption    string           `json:"Caption,omitempty"`
	Mimetype   string           `json:"Mimetype,omitempty"`
	FileLength uint64           `json:"FileLength,omitempty"`
	FileName   string           `json:"FileName,omitempty"`
	URL        string           `json:"URL,omitempty"`
	MediaKey   string           `json:"MediaKey,omitempty"`
	ContextInfo *wuzapiContextInfo `json:"ContextInfo,omitempty"`
}

type wuzapiLocation struct {
	DegreesLatitude  float64 `json:"DegreesLatitude"`
	DegreesLongitude float64 `json:"DegreesLongitude"`
	Name             string  `json:"Name,omitempty"`
	Address          string  `json:"Address,omitempty"`
}

type wuzapiContact struct {
	DisplayName string `json:"DisplayName,omitempty"`
	Vcard       string `json:"Vcard,omitempty"`
}

type wuzapiContextInfo struct {
	StanzaID    string `json:"StanzaId,omitempty"`
	Participant string `json:"Participant,omitempty"`
}

// NormalizerConfig holds settings needed by the normalizer.
type NormalizerConfig struct {
	WuzapiBaseURL      string
	WuzapiInstanceName string
}

// NormalizeInbound converts a raw Wuzapi webhook JSON payload into a
// NipperMessage. Returns (nil, nil) for messages that should be silently
// ignored (self-messages, non-Message events).
func NormalizeInbound(raw []byte, cfg NormalizerConfig) (*models.NipperMessage, error) {
	var wh WuzapiWebhook
	if err := json.Unmarshal(raw, &wh); err != nil {
		return nil, fmt.Errorf("whatsapp normalizer: unmarshal webhook: %w", err)
	}

	if wh.Type != "Message" {
		return nil, nil
	}

	// JSON webhook mode nests the payload inside "event".
	if wh.Event != nil {
		wh.Info = wh.Event.Info
		wh.Message = wh.Event.Message
		if wh.Event.S3 != nil {
			wh.S3 = wh.Event.S3
		}
	}

	if wh.Info.ResolvedIsFromMe() {
		return nil, nil
	}

	var msg wuzapiMessage
	if err := json.Unmarshal(wh.Message, &msg); err != nil {
		return nil, fmt.Errorf("whatsapp normalizer: unmarshal message: %w", err)
	}

	content, mediaType := extractContent(&msg, wh.S3)

	chatJID := normalizeJID(wh.Info.ResolvedChat())
	senderJID := normalizeJID(wh.Info.ResolvedSender())

	meta := models.WhatsAppMeta{
		WuzapiUserID:       wh.UserID,
		WuzapiInstanceName: wh.InstanceName,
		WuzapiBaseURL:      cfg.WuzapiBaseURL,
		ChatJID:            chatJID,
		SenderJID:          senderJID,
		MessageID:          wh.Info.ID,
		PushName:           wh.Info.PushName,
		IsGroup:            wh.Info.ResolvedIsGroup(),
		IsFromMe:           false,
		HasMedia:           mediaType != "",
		MediaType:          mediaType,
	}

	if wh.S3 != nil && wh.S3.URL != "" {
		meta.S3URL = wh.S3.URL
	}

	setQuotedContext(&msg, &meta)

	ts := time.Now().UTC()
	if wh.Info.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, wh.Info.Timestamp); err == nil {
			ts = parsed
		}
	}

	nm := &models.NipperMessage{
		MessageID:       uuid.New().String(),
		OriginMessageID: wh.Info.ID,
		ChannelType:     models.ChannelTypeWhatsApp,
		ChannelIdentity: senderJID,
		Content:         content,
		Meta:            meta,
		Timestamp:       ts,
		DeliveryContext: models.DeliveryContext{
			ChannelType:  models.ChannelTypeWhatsApp,
			ChannelID:    chatJID,
			ReplyMode:    "inline",
			Capabilities: models.WhatsAppCapabilities(),
		},
	}

	return nm, nil
}

// extractContent parses the wuzapiMessage into a MessageContent and
// returns the media type string (empty if text-only).
func extractContent(msg *wuzapiMessage, s3 *WuzapiS3) (models.MessageContent, string) {
	// Simple text conversation
	if msg.Conversation != "" {
		return models.MessageContent{Text: msg.Conversation}, ""
	}

	// Extended text (replies, links)
	if msg.ExtendedText != nil {
		return models.MessageContent{Text: msg.ExtendedText.Text}, ""
	}

	// Image
	if msg.ImageMessage != nil {
		return buildMediaContent(msg.ImageMessage, "image", s3), "image"
	}

	// Audio
	if msg.AudioMessage != nil {
		return buildMediaContent(msg.AudioMessage, "audio", s3), "audio"
	}

	// Video
	if msg.VideoMessage != nil {
		return buildMediaContent(msg.VideoMessage, "video", s3), "video"
	}

	// Document
	if msg.DocumentMessage != nil {
		return buildMediaContent(msg.DocumentMessage, "document", s3), "document"
	}

	// Sticker
	if msg.StickerMessage != nil {
		return buildMediaContent(msg.StickerMessage, "image", s3), "sticker"
	}

	// Location
	if msg.LocationMessage != nil {
		return models.MessageContent{
			Text: formatLocationText(msg.LocationMessage),
			Parts: []models.ContentPart{
				{
					Type:      "location",
					Latitude:  msg.LocationMessage.DegreesLatitude,
					Longitude: msg.LocationMessage.DegreesLongitude,
					Address:   msg.LocationMessage.Address,
				},
			},
		}, ""
	}

	// Contact
	if msg.ContactMessage != nil {
		text := msg.ContactMessage.DisplayName
		if text == "" {
			text = "Shared contact"
		}
		return models.MessageContent{
			Text: text,
			Parts: []models.ContentPart{
				{
					Type: "contact",
					Text: msg.ContactMessage.Vcard,
				},
			},
		}, ""
	}

	return models.MessageContent{}, ""
}

func buildMediaContent(media *wuzapiMediaMessage, partType string, s3 *WuzapiS3) models.MessageContent {
	url := ""
	if s3 != nil && s3.URL != "" {
		url = s3.URL
	}

	caption := media.Caption
	parts := []models.ContentPart{
		{
			Type:     partType,
			MimeType: media.Mimetype,
			URL:      url,
			Caption:  caption,
		},
	}

	if caption != "" {
		parts = append([]models.ContentPart{{Type: "text", Text: caption}}, parts...)
	}

	text := caption
	if text == "" {
		text = fmt.Sprintf("[%s message]", partType)
	}

	return models.MessageContent{
		Text:  text,
		Parts: parts,
	}
}

func setQuotedContext(msg *wuzapiMessage, meta *models.WhatsAppMeta) {
	var ci *wuzapiContextInfo
	switch {
	case msg.ExtendedText != nil && msg.ExtendedText.ContextInfo != nil:
		ci = msg.ExtendedText.ContextInfo
	case msg.ImageMessage != nil && msg.ImageMessage.ContextInfo != nil:
		ci = msg.ImageMessage.ContextInfo
	case msg.AudioMessage != nil && msg.AudioMessage.ContextInfo != nil:
		ci = msg.AudioMessage.ContextInfo
	case msg.VideoMessage != nil && msg.VideoMessage.ContextInfo != nil:
		ci = msg.VideoMessage.ContextInfo
	case msg.DocumentMessage != nil && msg.DocumentMessage.ContextInfo != nil:
		ci = msg.DocumentMessage.ContextInfo
	case msg.StickerMessage != nil && msg.StickerMessage.ContextInfo != nil:
		ci = msg.StickerMessage.ContextInfo
	}
	if ci != nil {
		meta.QuotedMessageID = ci.StanzaID
		meta.QuotedParticipant = ci.Participant
	}
}

// normalizeJID strips the device suffix from a WhatsApp JID.
// "12365122633:26@s.whatsapp.net" → "12365122633@s.whatsapp.net"
// "5491155553935@s.whatsapp.net"  → "5491155553935@s.whatsapp.net" (unchanged)
func normalizeJID(jid string) string {
	at := strings.Index(jid, "@")
	if at <= 0 {
		return jid
	}
	user := jid[:at]
	domain := jid[at:]
	if colon := strings.Index(user, ":"); colon > 0 {
		user = user[:colon]
	}
	return user + domain
}

func formatLocationText(loc *wuzapiLocation) string {
	if loc.Name != "" {
		return fmt.Sprintf("📍 %s", loc.Name)
	}
	return fmt.Sprintf("📍 %.6f, %.6f", loc.DegreesLatitude, loc.DegreesLongitude)
}
