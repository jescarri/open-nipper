package enrich

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-nipper/open-nipper/internal/models"
)

// Pipeline runs enrichers against all ContentParts in a message.
type Pipeline struct {
	enrichers []Enricher
}

// NewPipeline creates a pipeline from the given enrichers.
func NewPipeline(enrichers ...Enricher) *Pipeline {
	return &Pipeline{enrichers: enrichers}
}

// EnrichMessage processes all media parts in the message and injects results
// back into the message content. It mutates msg in-place.
//
// For audio parts: the transcript replaces (or is appended to) msg.Content.Text
// so the LLM sees the spoken words as if they were typed.
func (p *Pipeline) EnrichMessage(ctx context.Context, msg *models.NipperMessage) error {
	if len(p.enrichers) == 0 {
		return nil
	}

	var transcripts []string

	for i := range msg.Content.Parts {
		part := &msg.Content.Parts[i]

		for _, e := range p.enrichers {
			if !e.Supports(part.Type) {
				continue
			}

			result, err := e.Enrich(ctx, ContentPartView{
				Type:     part.Type,
				URL:      part.URL,
				MimeType: part.MimeType,
				Caption:  part.Caption,
			})
			if err != nil {
				return fmt.Errorf("enriching %s part %d: %w", part.Type, i, err)
			}

			if result != nil && result.Transcript != "" {
				part.Transcript = result.Transcript
				transcripts = append(transcripts, result.Transcript)
			}
			break // one enricher per part
		}
	}

	if len(transcripts) == 0 {
		return nil
	}

	// Inject transcripts into the message text.
	joined := strings.Join(transcripts, "\n")
	if msg.Content.Text == "" {
		// Audio-only message: transcript IS the text. No prefix needed.
		msg.Content.Text = joined
	} else {
		// Mixed message (text + audio): append with a label.
		msg.Content.Text = msg.Content.Text + "\n\n[Voice message]: " + joined
	}

	return nil
}
