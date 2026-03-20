package agent

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/pkg/session"
)

func TestStripControlChars(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"preserves normal text", "hello world", "hello world"},
		{"preserves newlines and tabs", "hello\n\tworld", "hello\n\tworld"},
		{"strips ETX (0x03)", "add a calendar invite:\x03\nuser@email.com", "add a calendar invite:\nuser@email.com"},
		{"strips NUL", "hello\x00world", "helloworld"},
		{"strips BEL SOH STX", "\x01\x02\x07text", "text"},
		{"preserves unicode", "héllo wörld 🌍", "héllo wörld 🌍"},
		{"empty string", "", ""},
		{"strips carriage return", "line1\r\nline2", "line1\nline2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripControlChars(tt.input)
			if got != tt.want {
				t.Errorf("stripControlChars(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNipperMessageToEinoMessage_StripsControlChars(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{Text: "hello\x03world"},
	}
	eino := NipperMessageToEinoMessage(msg)
	if strings.Contains(eino.Content, "\x03") {
		t.Error("expected ETX control char to be stripped")
	}
	if eino.Content != "helloworld" {
		t.Errorf("expected 'helloworld', got %q", eino.Content)
	}
}

func TestNipperMessageToEinoMessage_StripsControlCharsFromParts(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "text", Text: "text\x03from\x01part"},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)
	if eino.Content != "textfrompart" {
		t.Errorf("expected 'textfrompart', got %q", eino.Content)
	}
}

func TestTranscriptLinesToEinoMessages(t *testing.T) {
	lines := []session.TranscriptLine{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "unknown_role", Content: "should be skipped"},
		{Role: "tool", Content: "tool result"},
	}
	msgs := TranscriptLinesToEinoMessages(lines)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, tool), got %d", len(msgs))
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "Hello" {
		t.Errorf("unexpected first message: %+v", msgs[0])
	}
	if msgs[1].Role != schema.Assistant {
		t.Errorf("expected assistant role, got %s", msgs[1].Role)
	}
	if msgs[2].Role != schema.Tool {
		t.Errorf("expected tool role, got %s", msgs[2].Role)
	}
}

func TestEinoMessageToTranscriptLine(t *testing.T) {
	msg := &schema.Message{Role: schema.Assistant, Content: "response text"}
	line := EinoMessageToTranscriptLine(msg, "run-123")
	if line.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", line.Role)
	}
	if line.Content != "response text" {
		t.Errorf("expected 'response text', got %q", line.Content)
	}
	if line.RunID != "run-123" {
		t.Errorf("expected run-123, got %s", line.RunID)
	}
}

func TestNipperMessageToEinoMessage_TextOnly(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{Text: "just text"},
	}
	eino := NipperMessageToEinoMessage(msg)
	if eino.Role != schema.User {
		t.Errorf("expected user role, got %s", eino.Role)
	}
	if eino.Content != "just text" {
		t.Errorf("expected 'just text', got %q", eino.Content)
	}
}

func TestNipperMessageToEinoMessage_TextFromParts(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "text", Text: "text from part"},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)
	if eino.Content != "text from part" {
		t.Errorf("expected 'text from part', got %q", eino.Content)
	}
}

func TestNipperMessageToEinoMessage_MediaAnnotation(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "Check this document",
			Parts: []models.ContentPart{
				{Type: "text", Text: "Check this document"},
				{Type: "document", MimeType: "application/pdf", URL: "s3://bucket/docs/file.pdf", Caption: "quarterly report"},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)

	if !strings.Contains(eino.Content, "Check this document") {
		t.Error("expected original text to be present")
	}
	if !strings.Contains(eino.Content, "s3://bucket/docs/file.pdf") {
		t.Error("expected S3 URL annotation")
	}
	if !strings.Contains(eino.Content, "application/pdf") {
		t.Error("expected MIME type in annotation")
	}
	if !strings.Contains(eino.Content, "quarterly report") {
		t.Error("expected caption in annotation")
	}
	if !strings.Contains(eino.Content, "MUST call doc_fetch") {
		t.Error("expected mandatory fetch instruction")
	}
}

func TestNipperMessageToEinoMessage_ImageWithS3URL(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "[image message]",
			Parts: []models.ContentPart{
				{Type: "image", MimeType: "image/jpeg", URL: "s3://wuzapi-media/img-12345.jpg"},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)

	if !strings.Contains(eino.Content, "ATTACHED FILE: IMAGE") {
		t.Error("expected image annotation")
	}
	if !strings.Contains(eino.Content, "s3://wuzapi-media/img-12345.jpg") {
		t.Error("expected S3 URL in annotation")
	}
	if !strings.Contains(eino.Content, "MUST call doc_fetch") {
		t.Error("expected mandatory fetch instruction")
	}
}

func TestNipperMessageToEinoMessage_MultipleMediaParts(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "Here are files",
			Parts: []models.ContentPart{
				{Type: "image", MimeType: "image/png", URL: "s3://bucket/img.png"},
				{Type: "document", MimeType: "application/pdf", URL: "s3://bucket/doc.pdf"},
				{Type: "audio", MimeType: "audio/ogg", URL: "s3://bucket/voice.ogg"},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)

	if !strings.Contains(eino.Content, "ATTACHED FILE: IMAGE") {
		t.Error("expected image annotation")
	}
	if !strings.Contains(eino.Content, "ATTACHED FILE: DOCUMENT") {
		t.Error("expected document annotation")
	}
	if !strings.Contains(eino.Content, "ATTACHED FILE: AUDIO") {
		t.Error("expected audio annotation")
	}
}

func TestNipperMessageToEinoMessage_NoURLSkipsAnnotation(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "location message",
			Parts: []models.ContentPart{
				{Type: "location", Latitude: 40.7128, Longitude: -74.006},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)

	if strings.Contains(eino.Content, "ATTACHED FILE") {
		t.Error("should not annotate parts without URL")
	}
	if eino.Content != "location message" {
		t.Errorf("expected plain text, got %q", eino.Content)
	}
}

func TestNipperMessageToEinoMessage_MediaOnlyNoText(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "image", MimeType: "image/jpeg", URL: "s3://bucket/photo.jpg"},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)

	if !strings.Contains(eino.Content, "ATTACHED FILE: IMAGE") {
		t.Error("expected image annotation even without text")
	}
	if !strings.Contains(eino.Content, "s3://bucket/photo.jpg") {
		t.Error("expected S3 URL")
	}
}

func TestBuildMediaAnnotations_Empty(t *testing.T) {
	result := buildMediaAnnotations(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestBuildMediaAnnotations_TextPartsSkipped(t *testing.T) {
	parts := []models.ContentPart{
		{Type: "text", Text: "hello"},
	}
	result := buildMediaAnnotations(parts)
	if result != "" {
		t.Errorf("expected empty (text parts skipped), got %q", result)
	}
}

func TestBuildMediaAnnotations_NoURLSkipped(t *testing.T) {
	parts := []models.ContentPart{
		{Type: "image", MimeType: "image/jpeg"},
	}
	result := buildMediaAnnotations(parts)
	if result != "" {
		t.Errorf("expected empty (no URL), got %q", result)
	}
}

func TestBuildMediaAnnotations_DirectivePresent(t *testing.T) {
	parts := []models.ContentPart{
		{Type: "document", MimeType: "application/pdf", URL: "https://example.com/doc.pdf"},
	}
	result := buildMediaAnnotations(parts)
	if !strings.Contains(result, "MUST call doc_fetch") {
		t.Error("expected mandatory fetch instruction in annotation")
	}
	if !strings.Contains(result, "Do NOT guess") {
		t.Error("expected anti-hallucination instruction")
	}
}

func TestBuildMediaAnnotations_TranscribedAudioSkipped(t *testing.T) {
	parts := []models.ContentPart{
		{Type: "audio", MimeType: "audio/ogg", URL: "s3://bucket/voice.ogg", Transcript: "Hello world"},
	}
	result := buildMediaAnnotations(parts)
	if result != "" {
		t.Errorf("expected empty (transcribed audio should be skipped), got %q", result)
	}
}

func TestBuildMediaAnnotations_UntranscribedAudioKept(t *testing.T) {
	parts := []models.ContentPart{
		{Type: "audio", MimeType: "audio/ogg", URL: "s3://bucket/voice.ogg"},
	}
	result := buildMediaAnnotations(parts)
	if !strings.Contains(result, "ATTACHED FILE: AUDIO") {
		t.Error("expected audio annotation for untranscribed audio")
	}
}

func TestBuildMediaAnnotations_MixedTranscribedAndNot(t *testing.T) {
	parts := []models.ContentPart{
		{Type: "audio", MimeType: "audio/ogg", URL: "s3://bucket/voice.ogg", Transcript: "transcribed"},
		{Type: "document", MimeType: "application/pdf", URL: "s3://bucket/doc.pdf"},
	}
	result := buildMediaAnnotations(parts)
	if strings.Contains(result, "AUDIO") {
		t.Error("transcribed audio should not appear in annotations")
	}
	if !strings.Contains(result, "DOCUMENT") {
		t.Error("document annotation should still appear")
	}
}

func TestNipperMessageToEinoMessage_TranscribedAudioAsText(t *testing.T) {
	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "What is the weather?",
			Parts: []models.ContentPart{
				{Type: "audio", MimeType: "audio/ogg", URL: "s3://bucket/voice.ogg", Transcript: "What is the weather?"},
			},
		},
	}
	eino := NipperMessageToEinoMessage(msg)

	// The text should NOT contain any audio annotation since it was transcribed.
	if strings.Contains(eino.Content, "ATTACHED FILE") {
		t.Error("transcribed audio should not generate an attachment annotation")
	}
	if !strings.Contains(eino.Content, "What is the weather?") {
		t.Error("expected the text content to be present")
	}
}
