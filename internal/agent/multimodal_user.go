package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register PNG decoder
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/open-nipper/open-nipper/internal/config"
	"github.com/open-nipper/open-nipper/internal/models"
	"github.com/open-nipper/open-nipper/internal/s3fetch"
)

const (
	maxDownloadVisionImageBytes = 15 * 1024 * 1024 // 15MB max download for vision inlining
	downscaleThresholdBytes     = 1 * 1024 * 1024  // above this, downscale/re-encode to keep requests small
	maxVisionDim                = 1024             // max width/height for inline vision image
)

// fetchVisionMediaBytes is a test seam around fetchMediaBytesFromS3.
// It allows unit tests to provide deterministic image bytes without needing
// a running MinIO/S3 service.
var fetchVisionMediaBytes = fetchMediaBytesFromS3

// NipperMessageToEinoMessageWithInlineImages builds an Eino user message that:
// - Keeps the existing attachment annotations (so the model can call doc_fetch for EXIF).
// - Also attaches (at most) one image as inline base64 for vision-capable models.
//
// If the image cannot be fetched (no S3 config, unsupported URL, too large, etc.),
// it falls back to the regular text-only message.
func NipperMessageToEinoMessageWithInlineImages(
	ctx context.Context,
	msg *models.NipperMessage,
	s3Cfg config.S3DefaultConfig,
) (*schema.Message, bool, error) {
	// Always produce the same text the existing converter uses (including URL annotations).
	textOnly := NipperMessageToEinoMessage(msg)
	if textOnly == nil {
		return &schema.Message{Role: schema.User}, false, nil
	}

	// Find the first attachment that is an image (WhatsApp may send photos as "document" with image/jpeg).
	var imgPart *models.ContentPart
	for i := range msg.Content.Parts {
		p := &msg.Content.Parts[i]
		if p.URL == "" {
			continue
		}
		if isImageLikePart(*p) {
			imgPart = p
			break
		}
	}
	if imgPart == nil {
		return textOnly, false, nil
	}

	rawBytes, mime, err := fetchVisionMediaBytes(ctx, s3Cfg, imgPart.URL)
	if err != nil || len(rawBytes) == 0 {
		if err == nil {
			err = fmt.Errorf("empty image bytes")
		}
		return textOnly, false, fmt.Errorf("inline image fetch failed: %w", err)
	}

	b64, outMIME, err := prepareVisionImageBase64(rawBytes, mime)
	if err != nil || b64 == "" || outMIME == "" {
		if err == nil {
			err = fmt.Errorf("empty base64 or mime")
		}
		return textOnly, false, fmt.Errorf("inline image encode failed: %w", err)
	}

	// Eino's OpenAI-compatible adapter expects Image.URL to be set. For inline bytes,
	// the most portable representation is a data URL.
	//
	// See: eino-ext/components/model/openai README "generate_with_image".
	dataURL := fmt.Sprintf("data:%s;base64,%s", outMIME, b64)

	// The OpenAI wire format rejects a message that has both Content (string) and
	// MultiContent set simultaneously. The text is carried inside UserInputMultiContent
	// as a ChatMessagePartTypeText part; leave Content empty.
	//
	// Structure: [instructions + URL annotation] [image pixels] [EXIF reminder]
	// The post-image text part is placed last so it is the final thing the model reads
	// before generating a response. Small models tend to prioritize the most recent
	// context, so this placement maximises compliance with the doc_fetch requirement.
	exifReminder := fmt.Sprintf(
		"MANDATORY NEXT STEPS:\n"+
			"1. Call doc_fetch on the URL below to extract EXIF metadata (GPS coordinates, camera model, date/time). "+
			"EXIF data is encoded in the raw file bytes and is NOT visible in the image pixels. "+
			"You cannot determine GPS location from pixels alone.\n"+
			"2. AFTER calling doc_fetch, use your vision capabilities to DESCRIBE what you see in the image above. "+
			"You HAVE the image pixels — analyze the visual content (objects, scene, problems, context) "+
			"and combine it with the EXIF metadata in your response.\n"+
			"The URL to fetch is: %s", imgPart.URL,
	)

	return &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{
				Type: schema.ChatMessagePartTypeText,
				Text: textOnly.Content,
			},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						URL: &dataURL,
					},
					Detail: schema.ImageURLDetailAuto,
				},
			},
			{
				Type: schema.ChatMessagePartTypeText,
				Text: exifReminder,
			},
		},
	}, true, nil
}

func isImageLikePart(p models.ContentPart) bool {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.MimeType)), "image/") {
		return true
	}
	// Fallback: some senders omit MIME; treat explicit "image" type as image-like.
	return p.Type == "image"
}

func prepareVisionImageBase64(raw []byte, contentType string) (b64 string, outMIME string, err error) {
	// If it's already small enough, keep it as-is.
	if len(raw) <= downscaleThresholdBytes {
		mime := normalizeMIME(contentType)
		if mime == "" {
			mime = "image/jpeg"
		}
		enc := base64.StdEncoding.EncodeToString(raw)
		return enc, mime, nil
	}

	// Otherwise, decode and downscale, then encode as JPEG.
	img, _, decErr := image.Decode(bytes.NewReader(raw))
	if decErr != nil {
		return "", "", fmt.Errorf("decode image: %w", decErr)
	}

	scaled := resizeNearest(img, maxVisionDim)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 80}); err != nil {
		return "", "", fmt.Errorf("encode jpeg: %w", err)
	}

	enc := base64.StdEncoding.EncodeToString(buf.Bytes())
	return enc, "image/jpeg", nil
}

// resizeNearest scales img down so the longer side is at most maxDim using a
// simple nearest-neighbor resampler.
func resizeNearest(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return img
	}
	if w <= maxDim && h <= maxDim {
		return img
	}

	var nw, nh int
	if w >= h {
		nw = maxDim
		nh = (h * maxDim) / w
	} else {
		nh = maxDim
		nw = (w * maxDim) / h
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + (y*h)/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + (x*w)/nw
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	return dst
}

// fetchMediaBytesFromS3 downloads bytes using the shared s3fetch package.
// It handles s3:// URIs and HTTPS URLs whose host matches the S3 endpoint.
func fetchMediaBytesFromS3(ctx context.Context, s3Cfg config.S3DefaultConfig, rawURL string) ([]byte, string, error) {
	fetcher := s3fetch.NewFetcher(s3Cfg, s3fetch.WithMaxBytes(maxDownloadVisionImageBytes))
	data, err := fetcher.Fetch(ctx, rawURL)
	if err != nil {
		return nil, "", err
	}
	mime := normalizeMIME(httpDetectMIME(data))
	return data, mime, nil
}

func httpDetectMIME(data []byte) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "image/png"
	}
	return "application/octet-stream"
}

func normalizeMIME(ct string) string {
	ct = strings.ToLower(ct)
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}
	return strings.TrimSpace(ct)
}

var _ = color.RGBA{} // keep image/color import used even when build tags vary
