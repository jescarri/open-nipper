package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRequestDeviceCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		if got := r.FormValue("client_id"); got != "test-client" {
			t.Errorf("got client_id %q, want %q", got, "test-client")
		}
		if got := r.FormValue("scope"); got != "openid profile" {
			t.Errorf("got scope %q, want %q", got, "openid profile")
		}
		if got := r.FormValue("audience"); got != "https://api.example.com" {
			t.Errorf("got audience %q, want %q", got, "https://api.example.com")
		}

		resp := DeviceAuthResponse{
			DeviceCode:              "dev-code-123",
			UserCode:                "ABCD-EFGH",
			VerificationURI:         "https://example.com/activate",
			VerificationURIComplete: "https://example.com/activate?user_code=ABCD-EFGH",
			ExpiresIn:               1800,
			Interval:                5,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{
		DeviceAuthorizationEndpoint: srv.URL,
		TokenEndpoint:               "https://unused.example.com/token",
	}
	cfg := &DeviceFlowConfig{
		ClientID: "test-client",
		Scopes:   []string{"openid", "profile"},
		Audience: "https://api.example.com",
	}

	resp, err := RequestDeviceCode(context.Background(), endpoints, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.DeviceCode != "dev-code-123" {
		t.Errorf("got device code %q, want %q", resp.DeviceCode, "dev-code-123")
	}
	if resp.UserCode != "ABCD-EFGH" {
		t.Errorf("got user code %q, want %q", resp.UserCode, "ABCD-EFGH")
	}
	if resp.VerificationURIComplete != "https://example.com/activate?user_code=ABCD-EFGH" {
		t.Errorf("got verification_uri_complete %q", resp.VerificationURIComplete)
	}
}

func TestRequestDeviceCode_WithClientSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		if got := r.FormValue("client_secret"); got != "secret-123" {
			t.Errorf("got client_secret %q, want %q", got, "secret-123")
		}
		resp := DeviceAuthResponse{DeviceCode: "dc", UserCode: "UC", VerificationURI: "https://v.com", ExpiresIn: 60, Interval: 5}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{DeviceAuthorizationEndpoint: srv.URL, TokenEndpoint: "https://unused.com"}
	cfg := &DeviceFlowConfig{ClientID: "c", ClientSecret: "secret-123"}
	_, err := RequestDeviceCode(context.Background(), endpoints, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestDeviceCode_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{DeviceAuthorizationEndpoint: srv.URL, TokenEndpoint: "https://unused.com"}
	cfg := &DeviceFlowConfig{ClientID: "c"}
	_, err := RequestDeviceCode(context.Background(), endpoints, cfg)
	if err == nil {
		t.Fatal("expected error on server error")
	}
}

func TestExchangeDeviceCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			AccessToken:  "access-token-xyz",
			TokenType:    "Bearer",
			RefreshToken: "refresh-token-abc",
			ExpiresIn:    3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{
		DeviceAuthorizationEndpoint: "https://unused.com",
		TokenEndpoint:               srv.URL,
	}
	cfg := &DeviceFlowConfig{ClientID: "test-client"}

	before := time.Now()
	result := exchangeDeviceCode(context.Background(), endpoints, cfg, "device-code")
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if result.token == nil {
		t.Fatal("expected token, got nil")
	}
	if result.token.AccessToken != "access-token-xyz" {
		t.Errorf("got access token %q, want %q", result.token.AccessToken, "access-token-xyz")
	}
	if result.token.TokenType != "Bearer" {
		t.Errorf("got token type %q, want %q", result.token.TokenType, "Bearer")
	}
	if result.token.RefreshToken != "refresh-token-abc" {
		t.Errorf("got refresh token %q, want %q", result.token.RefreshToken, "refresh-token-abc")
	}
	expectedExpiry := before.Add(3600 * time.Second)
	if result.token.Expiry.Before(expectedExpiry.Add(-2*time.Second)) || result.token.Expiry.After(expectedExpiry.Add(2*time.Second)) {
		t.Errorf("expiry %v not within expected range around %v", result.token.Expiry, expectedExpiry)
	}
}

func TestExchangeDeviceCode_AuthorizationPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{Error: "authorization_pending"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{TokenEndpoint: srv.URL}
	cfg := &DeviceFlowConfig{ClientID: "c"}

	result := exchangeDeviceCode(context.Background(), endpoints, cfg, "dc")
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if result.token != nil {
		t.Error("expected nil token for authorization_pending")
	}
	if result.increaseDelay {
		t.Error("should not increase delay for authorization_pending")
	}
}

func TestExchangeDeviceCode_SlowDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{Error: "slow_down"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{TokenEndpoint: srv.URL}
	cfg := &DeviceFlowConfig{ClientID: "c"}

	result := exchangeDeviceCode(context.Background(), endpoints, cfg, "dc")
	if result.err != nil {
		t.Fatalf("unexpected error: %v", result.err)
	}
	if !result.increaseDelay {
		t.Error("expected increaseDelay=true for slow_down")
	}
}

func TestExchangeDeviceCode_ExpiredToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{Error: "expired_token"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{TokenEndpoint: srv.URL}
	cfg := &DeviceFlowConfig{ClientID: "c"}

	result := exchangeDeviceCode(context.Background(), endpoints, cfg, "dc")
	if result.err == nil {
		t.Fatal("expected error for expired_token")
	}
	if result.err.Error() != "device code expired" {
		t.Errorf("got error %q, want %q", result.err.Error(), "device code expired")
	}
}

func TestExchangeDeviceCode_AccessDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{Error: "access_denied"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{TokenEndpoint: srv.URL}
	cfg := &DeviceFlowConfig{ClientID: "c"}

	result := exchangeDeviceCode(context.Background(), endpoints, cfg, "dc")
	if result.err == nil {
		t.Fatal("expected error for access_denied")
	}
	if result.err.Error() != "access denied by user" {
		t.Errorf("got error %q, want %q", result.err.Error(), "access denied by user")
	}
}

func TestPollForToken_SuccessAfterPending(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		var resp tokenResponse
		if n < 3 {
			resp = tokenResponse{Error: "authorization_pending"}
		} else {
			resp = tokenResponse{
				AccessToken:  "final-token",
				TokenType:    "Bearer",
				RefreshToken: "refresh",
				ExpiresIn:    3600,
			}
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{
		DeviceAuthorizationEndpoint: "https://unused.com",
		TokenEndpoint:               srv.URL,
	}
	cfg := &DeviceFlowConfig{ClientID: "test-client"}
	deviceResp := &DeviceAuthResponse{
		DeviceCode:      "dc",
		UserCode:        "UC-1234",
		VerificationURI: "https://example.com/activate",
		ExpiresIn:       30,
		Interval:        1, // will be clamped to 5, but we override below
	}

	logger, _ := zap.NewDevelopment()

	// Use a short interval for testing: override the minimum by setting Interval >= 5
	// but that would make the test slow. Instead we test with Interval=1 which gets
	// clamped to 5. For a fast test, we rely on the unit tests of exchangeDeviceCode
	// above and test PollForToken with a context timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Override interval to be small for test speed - set to 5 (minimum)
	deviceResp.Interval = 5

	token, err := PollForToken(ctx, endpoints, cfg, deviceResp, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token.AccessToken != "final-token" {
		t.Errorf("got access token %q, want %q", token.AccessToken, "final-token")
	}
}

func TestPollForToken_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{Error: "authorization_pending"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints := &OIDCEndpoints{TokenEndpoint: srv.URL}
	cfg := &DeviceFlowConfig{ClientID: "c"}
	deviceResp := &DeviceAuthResponse{
		DeviceCode:      "dc",
		UserCode:        "UC",
		VerificationURI: "https://v.com",
		ExpiresIn:       300,
		Interval:        5,
	}

	logger, _ := zap.NewDevelopment()
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the first select picks it up
	cancel()

	_, err := PollForToken(ctx, endpoints, cfg, deviceResp, logger)
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
}
