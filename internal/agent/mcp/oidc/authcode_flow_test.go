package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestGeneratePKCE(t *testing.T) {
	pkce, err := generatePKCE()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkce.Verifier == "" {
		t.Fatal("verifier should not be empty")
	}
	if pkce.Challenge == "" {
		t.Fatal("challenge should not be empty")
	}
	if pkce.Method != "S256" {
		t.Errorf("method = %q, want S256", pkce.Method)
	}
	// Verifier and challenge must differ (challenge is SHA-256 of verifier).
	if pkce.Verifier == pkce.Challenge {
		t.Error("verifier and challenge should differ")
	}
}

func TestGenerateState(t *testing.T) {
	s1, err := generateState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s2, _ := generateState()
	if s1 == "" || s2 == "" {
		t.Fatal("state should not be empty")
	}
	if s1 == s2 {
		t.Error("two generated states should differ")
	}
}

func TestBuildAuthURL(t *testing.T) {
	pkce := &pkceChallenge{
		Verifier:  "test-verifier",
		Challenge: "test-challenge",
		Method:    "S256",
	}
	cfg := &AuthCodeFlowConfig{
		ClientID: "my-client",
		Scopes:   []string{"openid", "email", "https://www.googleapis.com/auth/calendar"},
	}

	u, err := buildAuthURL("https://accounts.google.com/o/oauth2/v2/auth", cfg, "http://127.0.0.1:9999/callback", "random-state", pkce)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("failed to parse URL: %v", err)
	}

	q := parsed.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
	if q.Get("client_id") != "my-client" {
		t.Errorf("client_id = %q, want my-client", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != "http://127.0.0.1:9999/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != "random-state" {
		t.Errorf("state = %q, want random-state", q.Get("state"))
	}
	if q.Get("code_challenge") != "test-challenge" {
		t.Errorf("code_challenge = %q", q.Get("code_challenge"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline", q.Get("access_type"))
	}
	if q.Get("scope") != "openid email https://www.googleapis.com/auth/calendar" {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestExchangeAuthCode(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "test-code" {
			t.Errorf("code = %q, want test-code", r.Form.Get("code"))
		}
		if r.Form.Get("code_verifier") == "" {
			t.Error("code_verifier should be present")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-12345",
			"token_type":    "Bearer",
			"refresh_token": "rt-67890",
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()

	cfg := &AuthCodeFlowConfig{
		ClientID:     "my-client",
		ClientSecret: "my-secret",
	}
	pkce := &pkceChallenge{Verifier: "test-verifier", Challenge: "test-challenge", Method: "S256"}

	token, err := exchangeAuthCode(context.Background(), tokenSrv.URL, cfg, "test-code", "http://127.0.0.1:9999/callback", pkce)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token.AccessToken != "at-12345" {
		t.Errorf("access_token = %q, want at-12345", token.AccessToken)
	}
	if token.RefreshToken != "rt-67890" {
		t.Errorf("refresh_token = %q, want rt-67890", token.RefreshToken)
	}
	if token.Expiry.Before(time.Now()) {
		t.Error("token should not be expired")
	}
}

func TestRunAuthCodeFlow_Success(t *testing.T) {
	// Fake token endpoint.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-flow-test",
			"token_type":    "Bearer",
			"refresh_token": "rt-flow-test",
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()

	endpoints := &OIDCEndpoints{
		AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth", // not actually used in test
		TokenEndpoint:         tokenSrv.URL,
	}
	cfg := &AuthCodeFlowConfig{
		ClientID: "test-client",
		Scopes:   []string{"openid"},
	}

	// Use a channel to receive the auth URL from the notifier (avoids data race).
	authURLCh := make(chan string, 1)
	notifier := DeviceAuthNotifier(func(_ context.Context, _, verificationURI, _ string, _ int) {
		authURLCh <- verificationURI
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run the flow in a goroutine since it blocks waiting for callback.
	type result struct {
		token *StoredToken
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		tok, err := RunAuthCodeFlow(ctx, endpoints, cfg, "test-server", testLogger(t), notifier)
		resultCh <- result{tok, err}
	}()

	// Wait for the notifier to fire with the auth URL.
	var capturedAuthURL string
	select {
	case capturedAuthURL = <-authURLCh:
	case <-time.After(5 * time.Second):
		t.Fatal("notifier was not called with auth URL")
	}

	if capturedAuthURL == "" {
		t.Fatal("notifier was not called with auth URL")
	}

	// Parse the auth URL to extract state and redirect_uri.
	parsed, err := url.Parse(capturedAuthURL)
	if err != nil {
		t.Fatalf("failed to parse auth URL: %v", err)
	}
	q := parsed.Query()
	state := q.Get("state")
	redirectURI := q.Get("redirect_uri")

	if state == "" || redirectURI == "" {
		t.Fatalf("auth URL missing state or redirect_uri: %s", capturedAuthURL)
	}

	// Simulate the browser callback.
	callbackURL := fmt.Sprintf("%s?code=test-auth-code&state=%s", redirectURI, url.QueryEscape(state))
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	resp.Body.Close()

	// Wait for the result.
	r := <-resultCh
	if r.err != nil {
		t.Fatalf("unexpected error: %v", r.err)
	}
	if r.token.AccessToken != "at-flow-test" {
		t.Errorf("access_token = %q, want at-flow-test", r.token.AccessToken)
	}
	if r.token.RefreshToken != "rt-flow-test" {
		t.Errorf("refresh_token = %q, want rt-flow-test", r.token.RefreshToken)
	}
}

func TestRunAuthCodeFlow_StateMismatch(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("token endpoint should not be called on state mismatch")
	}))
	defer tokenSrv.Close()

	endpoints := &OIDCEndpoints{
		AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenEndpoint:         tokenSrv.URL,
	}
	cfg := &AuthCodeFlowConfig{
		ClientID: "test-client",
	}

	authURLCh := make(chan string, 1)
	notifier := DeviceAuthNotifier(func(_ context.Context, _, verificationURI, _ string, _ int) {
		authURLCh <- verificationURI
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		token *StoredToken
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		tok, err := RunAuthCodeFlow(ctx, endpoints, cfg, "test-server", testLogger(t), notifier)
		resultCh <- result{tok, err}
	}()

	var capturedAuthURL string
	select {
	case capturedAuthURL = <-authURLCh:
	case <-time.After(5 * time.Second):
		t.Fatal("notifier was not called with auth URL")
	}

	parsed, _ := url.Parse(capturedAuthURL)
	redirectURI := parsed.Query().Get("redirect_uri")

	// Send callback with wrong state.
	callbackURL := fmt.Sprintf("%s?code=test-code&state=wrong-state", redirectURI)
	resp, _ := http.Get(callbackURL)
	if resp != nil {
		resp.Body.Close()
	}

	r := <-resultCh
	if r.err == nil {
		t.Fatal("expected error on state mismatch")
	}
}

func TestRunAuthCodeFlow_Timeout(t *testing.T) {
	endpoints := &OIDCEndpoints{
		AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenEndpoint:         "https://accounts.google.com/o/oauth2/token",
	}
	cfg := &AuthCodeFlowConfig{
		ClientID: "test-client",
	}

	// Use a very short context to trigger timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := RunAuthCodeFlow(ctx, endpoints, cfg, "test-server", testLogger(t), nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func testLogger(t *testing.T) *zap.Logger {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	return logger
}
