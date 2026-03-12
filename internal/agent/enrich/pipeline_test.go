package enrich

import (
	"context"
	"fmt"
	"testing"

	"github.com/jescarri/open-nipper/internal/models"
)

// mockEnricher is a test enricher that returns a fixed transcript.
type mockEnricher struct {
	supportType string
	transcript  string
	err         error
}

func (m *mockEnricher) Supports(contentType string) bool {
	return contentType == m.supportType
}

func (m *mockEnricher) Enrich(_ context.Context, _ ContentPartView) (*EnrichmentResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &EnrichmentResult{Transcript: m.transcript}, nil
}

func TestPipeline_EnrichMessage_AudioOnly(t *testing.T) {
	pipeline := NewPipeline(&mockEnricher{
		supportType: "audio",
		transcript:  "Hello, what is the weather?",
	})

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "audio", URL: "s3://bucket/voice.ogg", MimeType: "audio/ogg"},
			},
		},
	}

	err := pipeline.EnrichMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Transcript should become the message text directly.
	if msg.Content.Text != "Hello, what is the weather?" {
		t.Errorf("expected transcript as text, got %q", msg.Content.Text)
	}

	// Part should have Transcript set for bookkeeping.
	if msg.Content.Parts[0].Transcript != "Hello, what is the weather?" {
		t.Errorf("expected part.Transcript set, got %q", msg.Content.Parts[0].Transcript)
	}
}

func TestPipeline_EnrichMessage_AudioWithExistingText(t *testing.T) {
	pipeline := NewPipeline(&mockEnricher{
		supportType: "audio",
		transcript:  "remind me to buy groceries",
	})

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "listen to this",
			Parts: []models.ContentPart{
				{Type: "audio", URL: "s3://bucket/voice.ogg", MimeType: "audio/ogg"},
			},
		},
	}

	err := pipeline.EnrichMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "listen to this\n\n[Voice message]: remind me to buy groceries"
	if msg.Content.Text != expected {
		t.Errorf("expected %q, got %q", expected, msg.Content.Text)
	}
}

func TestPipeline_EnrichMessage_NoAudioParts(t *testing.T) {
	pipeline := NewPipeline(&mockEnricher{
		supportType: "audio",
		transcript:  "should not appear",
	})

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "just text",
			Parts: []models.ContentPart{
				{Type: "text", Text: "just text"},
			},
		},
	}

	err := pipeline.EnrichMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if msg.Content.Text != "just text" {
		t.Errorf("text should be unchanged, got %q", msg.Content.Text)
	}
}

func TestPipeline_EnrichMessage_EnricherError(t *testing.T) {
	pipeline := NewPipeline(&mockEnricher{
		supportType: "audio",
		err:         fmt.Errorf("whisper server down"),
	})

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "audio", URL: "s3://bucket/voice.ogg", MimeType: "audio/ogg"},
			},
		},
	}

	err := pipeline.EnrichMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if msg.Content.Text != "" {
		t.Errorf("text should remain empty on error, got %q", msg.Content.Text)
	}
}

func TestPipeline_EnrichMessage_NilPipeline(t *testing.T) {
	pipeline := NewPipeline() // no enrichers

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "audio", URL: "s3://bucket/voice.ogg"},
			},
		},
	}

	err := pipeline.EnrichMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content.Text != "" {
		t.Errorf("text should remain empty with no enrichers, got %q", msg.Content.Text)
	}
}

func TestPipeline_EnrichMessage_MultipleAudioParts(t *testing.T) {
	pipeline := NewPipeline(&mockEnricher{
		supportType: "audio",
		transcript:  "transcribed",
	})

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Parts: []models.ContentPart{
				{Type: "audio", URL: "s3://bucket/voice1.ogg", MimeType: "audio/ogg"},
				{Type: "audio", URL: "s3://bucket/voice2.ogg", MimeType: "audio/ogg"},
			},
		},
	}

	err := pipeline.EnrichMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "transcribed\ntranscribed"
	if msg.Content.Text != expected {
		t.Errorf("expected %q, got %q", expected, msg.Content.Text)
	}
}

func TestPipeline_EnrichMessage_MixedParts(t *testing.T) {
	pipeline := NewPipeline(&mockEnricher{
		supportType: "audio",
		transcript:  "hello from audio",
	})

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "check the files",
			Parts: []models.ContentPart{
				{Type: "image", URL: "s3://bucket/photo.jpg", MimeType: "image/jpeg"},
				{Type: "audio", URL: "s3://bucket/voice.ogg", MimeType: "audio/ogg"},
			},
		},
	}

	err := pipeline.EnrichMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "check the files\n\n[Voice message]: hello from audio"
	if msg.Content.Text != expected {
		t.Errorf("expected %q, got %q", expected, msg.Content.Text)
	}

	// Image part should be untouched.
	if msg.Content.Parts[0].Transcript != "" {
		t.Errorf("image part should have no transcript, got %q", msg.Content.Parts[0].Transcript)
	}
}
