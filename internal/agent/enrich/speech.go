package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/jescarri/open-nipper/internal/config"
	"github.com/jescarri/open-nipper/internal/s3fetch"
)

const (
	defaultSpeechTimeout = 60 * time.Second
	maxAudioDownload     = 25 * 1024 * 1024 // 25 MB
)

// whisperResponse is the JSON response from whisper.cpp server /inference.
type whisperResponse struct {
	Text string `json:"text"`
}

// SpeechEnricher transcribes audio parts using a whisper.cpp-compatible server.
// The server exposes POST /inference with multipart form data.
type SpeechEnricher struct {
	Endpoint   string // e.g. "https://speech.example.com"
	HTTPClient *http.Client

	// s3Fetcher fetches audio bytes from S3/Minio or plain HTTP URLs.
	s3Fetcher *s3fetch.Fetcher

	// FetchAudioFunc fetches audio bytes from a URL. Injected for testing.
	// If nil, uses the s3Fetcher (or plain HTTP fallback).
	FetchAudioFunc func(ctx context.Context, url string) ([]byte, error)
}

// NewSpeechEnricher creates a SpeechEnricher targeting the given whisper.cpp endpoint.
// The S3 config is used to authenticate downloads from Minio/S3 media URLs.
func NewSpeechEnricher(endpoint string, timeout time.Duration, s3Cfg config.S3DefaultConfig) *SpeechEnricher {
	if timeout <= 0 {
		timeout = defaultSpeechTimeout
	}
	httpClient := &http.Client{Timeout: timeout}
	return &SpeechEnricher{
		Endpoint:   strings.TrimRight(endpoint, "/"),
		HTTPClient: httpClient,
		s3Fetcher:  s3fetch.NewFetcher(s3Cfg, s3fetch.WithHTTPClient(httpClient), s3fetch.WithMaxBytes(maxAudioDownload)),
	}
}

func (s *SpeechEnricher) Supports(contentType string) bool {
	return contentType == "audio"
}

func (s *SpeechEnricher) Enrich(ctx context.Context, part ContentPartView) (*EnrichmentResult, error) {
	if part.URL == "" {
		return nil, fmt.Errorf("audio part has no URL")
	}

	fetchFn := s.FetchAudioFunc
	if fetchFn == nil {
		fetchFn = s.s3Fetcher.Fetch
	}

	audioBytes, err := fetchFn(ctx, part.URL)
	if err != nil {
		return nil, fmt.Errorf("fetching audio: %w", err)
	}

	transcript, err := s.transcribe(ctx, audioBytes)
	if err != nil {
		return nil, fmt.Errorf("transcribing audio: %w", err)
	}

	return &EnrichmentResult{Transcript: transcript}, nil
}

// transcribe sends audio bytes to the whisper.cpp /inference endpoint.
func (s *SpeechEnricher) transcribe(ctx context.Context, audio []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "audio.ogg")
	if err != nil {
		return "", fmt.Errorf("creating form file: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("writing audio data: %w", err)
	}

	_ = writer.WriteField("temperature", "0.0")
	_ = writer.WriteField("temperature_inc", "0.2")
	_ = writer.WriteField("response_format", "json")

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("closing multipart writer: %w", err)
	}

	url := s.Endpoint + "/inference"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper server returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result whisperResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing whisper response: %w", err)
	}

	return strings.TrimSpace(result.Text), nil
}
