// Package agent implements the Open-Nipper Go agent using the Eino SDK.
package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/jescarri/open-nipper/internal/models"
	"github.com/jescarri/open-nipper/pkg/session"
)

// TranscriptLinesToEinoMessages converts stored transcript lines to Eino messages.
// Lines with unrecognised roles are skipped.
func TranscriptLinesToEinoMessages(lines []session.TranscriptLine) []*schema.Message {
	out := make([]*schema.Message, 0, len(lines))
	for _, l := range lines {
		role := schema.RoleType(l.Role)
		switch role {
		case schema.User, schema.Assistant, schema.System, schema.Tool:
		default:
			continue
		}
		out = append(out, &schema.Message{
			Role:    role,
			Content: l.Content,
		})
	}
	return out
}

// EinoMessageToTranscriptLine converts an Eino message to a storable transcript line.
func EinoMessageToTranscriptLine(msg *schema.Message, runID string) session.TranscriptLine {
	return session.TranscriptLine{
		Role:      string(msg.Role),
		Content:   msg.Content,
		Timestamp: time.Now().UTC(),
		RunID:     runID,
	}
}

// NipperMessageToEinoMessage converts an inbound NipperMessage to an Eino user message.
// Text content takes precedence; media attachment context is appended so the LLM
// can see S3/HTTP URLs from WhatsApp media and invoke doc_fetch to retrieve them.
func NipperMessageToEinoMessage(msg *models.NipperMessage) *schema.Message {
	text := msg.Content.Text
	if text == "" {
		for _, p := range msg.Content.Parts {
			if p.Type == "text" && p.Text != "" {
				text = p.Text
				break
			}
		}
	}

	mediaAnnotations := buildMediaAnnotations(msg.Content.Parts)
	if mediaAnnotations != "" {
		if text != "" {
			text = text + "\n\n" + mediaAnnotations
		} else {
			text = mediaAnnotations
		}
	}

	return &schema.Message{
		Role:    schema.User,
		Content: text,
	}
}

func isImageMIME(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "image/")
}

// buildMediaAnnotations creates directive annotations for non-text content parts
// that have URLs (e.g. S3/HTTP URLs from WhatsApp media uploads). The annotations
// explicitly instruct the LLM to call doc_fetch — passive annotations cause the
// LLM to hallucinate file contents instead of fetching them.
func buildMediaAnnotations(parts []models.ContentPart) string {
	var annotations []string
	for _, p := range parts {
		if p.Type == "text" || p.URL == "" {
			continue
		}
		// Skip audio parts that were already transcribed by the enrichment pipeline.
		if p.Type == "audio" && p.Transcript != "" {
			continue
		}

		mime := p.MimeType
		if mime == "" {
			mime = "unknown"
		}

		annotation := fmt.Sprintf("⚠️ ATTACHED FILE: %s (%s)", strings.ToUpper(p.Type), mime)
		if p.Caption != "" {
			annotation += fmt.Sprintf("\nCaption: %s", p.Caption)
		}
		annotation += fmt.Sprintf("\nURL: %s", p.URL)
		if isImageMIME(mime) || p.Type == "image" {
			annotation += "\n→ You MUST call doc_fetch with this URL to extract EXIF metadata (GPS location, camera model, date/time)." +
				" EXIF is embedded in the raw file and is NOT visible in the image pixels." +
				" Do NOT claim there is no EXIF without calling doc_fetch first."
		} else {
			annotation += "\n→ You MUST call doc_fetch with this URL to access the file content. Do NOT guess, describe, or fabricate the content."
		}
		annotations = append(annotations, annotation)
	}

	if len(annotations) == 0 {
		return ""
	}
	return strings.Join(annotations, "\n\n")
}
