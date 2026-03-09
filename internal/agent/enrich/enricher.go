// Package enrich provides a media enrichment pipeline that processes
// non-text ContentParts (audio, image, video) before the LLM sees them.
// Each Enricher handles a specific media type and returns structured results
// that are injected back into the message as text.
package enrich

import "context"

// Enricher processes a single media ContentPart and returns enrichment data.
type Enricher interface {
	// Supports returns true if this enricher handles the given content type.
	Supports(contentType string) bool

	// Enrich fetches/processes the media and returns structured results.
	Enrich(ctx context.Context, part ContentPartView) (*EnrichmentResult, error)
}

// ContentPartView is a read-only view of a ContentPart for enrichers.
type ContentPartView struct {
	Type     string
	URL      string
	MimeType string
	Caption  string
}

// EnrichmentResult carries the output of an enricher.
type EnrichmentResult struct {
	// Transcript is the speech-to-text output (for audio).
	Transcript string
}
