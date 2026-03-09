package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/models"
)

func TestNipperMessageToEinoMessageWithInlineImages_UsesDataURL(t *testing.T) {
	// Build a tiny deterministic JPEG in-memory.
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	img.Set(1, 0, color.RGBA{R: 40, G: 50, B: 60, A: 255})
	img.Set(0, 1, color.RGBA{R: 70, G: 80, B: 90, A: 255})
	img.Set(1, 1, color.RGBA{R: 100, G: 110, B: 120, A: 255})

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	raw := buf.Bytes()
	wantB64 := base64.StdEncoding.EncodeToString(raw)

	origFetch := fetchVisionMediaBytes
	fetchVisionMediaBytes = func(_ context.Context, _ config.S3DefaultConfig, _ string) ([]byte, string, error) {
		return raw, "image/jpeg", nil
	}
	defer func() { fetchVisionMediaBytes = origFetch }()

	msg := &models.NipperMessage{
		Content: models.MessageContent{
			Text: "analyze this image",
			Parts: []models.ContentPart{
				{Type: "image", MimeType: "image/jpeg", URL: "s3://bucket/photo.jpg"},
			},
		},
	}

	out, ok, err := NipperMessageToEinoMessageWithInlineImages(context.Background(), msg, config.S3DefaultConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if out.Role != schema.User {
		t.Fatalf("expected user role, got %s", out.Role)
	}
	// Content must be empty — setting both Content and UserInputMultiContent
	// simultaneously causes the OpenAI adapter to fail serialization.
	if out.Content != "" {
		t.Fatalf("expected Content to be empty for multipart message, got %q", out.Content)
	}
	// Expect: [text instructions] [image] [EXIF reminder]
	if len(out.UserInputMultiContent) != 3 {
		t.Fatalf("expected 3 multipart parts (text, image, exif reminder), got %d", len(out.UserInputMultiContent))
	}
	if out.UserInputMultiContent[0].Type != schema.ChatMessagePartTypeText {
		t.Fatalf("expected first part to be text, got %v", out.UserInputMultiContent[0].Type)
	}
	if out.UserInputMultiContent[1].Type != schema.ChatMessagePartTypeImageURL {
		t.Fatalf("expected second part to be image_url, got %v", out.UserInputMultiContent[1].Type)
	}
	if out.UserInputMultiContent[1].Image == nil || out.UserInputMultiContent[1].Image.URL == nil {
		t.Fatalf("expected image url to be set")
	}
	if out.UserInputMultiContent[2].Type != schema.ChatMessagePartTypeText {
		t.Fatalf("expected third part to be text (exif reminder), got %v", out.UserInputMultiContent[2].Type)
	}
	if !strings.Contains(out.UserInputMultiContent[2].Text, "doc_fetch") {
		t.Fatalf("expected EXIF reminder to mention doc_fetch, got %q", out.UserInputMultiContent[2].Text)
	}
	if !strings.Contains(out.UserInputMultiContent[2].Text, "s3://bucket/photo.jpg") {
		t.Fatalf("expected EXIF reminder to include the image URL")
	}

	gotURL := *out.UserInputMultiContent[1].Image.URL
	if !strings.HasPrefix(gotURL, "data:image/jpeg;base64,") {
		t.Fatalf("expected data URL prefix, got %q", gotURL[:min(40, len(gotURL))])
	}
	if !strings.Contains(gotURL, wantB64[:min(30, len(wantB64))]) {
		t.Fatalf("expected data URL to contain base64-encoded JPEG bytes")
	}
}
