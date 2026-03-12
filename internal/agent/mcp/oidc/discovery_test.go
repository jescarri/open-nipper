package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscover_FromWellKnown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		resp := map[string]string{
			"device_authorization_endpoint": "https://example.com/device/code",
			"token_endpoint":                "https://example.com/oauth/token",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints, err := Discover(context.Background(), srv.URL, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if endpoints.DeviceAuthorizationEndpoint != "https://example.com/device/code" {
		t.Errorf("got device auth endpoint %q, want %q", endpoints.DeviceAuthorizationEndpoint, "https://example.com/device/code")
	}
	if endpoints.TokenEndpoint != "https://example.com/oauth/token" {
		t.Errorf("got token endpoint %q, want %q", endpoints.TokenEndpoint, "https://example.com/oauth/token")
	}
}

func TestDiscover_ExplicitOverrides(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]string{
			"device_authorization_endpoint": "https://example.com/device/code",
			"token_endpoint":                "https://example.com/oauth/token",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	endpoints, err := Discover(context.Background(), srv.URL, "", "https://override.com/device", "https://override.com/token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if endpoints.DeviceAuthorizationEndpoint != "https://override.com/device" {
		t.Errorf("got device auth endpoint %q, want explicit override", endpoints.DeviceAuthorizationEndpoint)
	}
	if endpoints.TokenEndpoint != "https://override.com/token" {
		t.Errorf("got token endpoint %q, want explicit override", endpoints.TokenEndpoint)
	}
}

func TestDiscover_FallbackNoIssuer(t *testing.T) {
	endpoints, err := Discover(context.Background(), "", "", "https://explicit.com/device", "https://explicit.com/token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if endpoints.DeviceAuthorizationEndpoint != "https://explicit.com/device" {
		t.Errorf("got device auth endpoint %q, want %q", endpoints.DeviceAuthorizationEndpoint, "https://explicit.com/device")
	}
	if endpoints.TokenEndpoint != "https://explicit.com/token" {
		t.Errorf("got token endpoint %q, want %q", endpoints.TokenEndpoint, "https://explicit.com/token")
	}
}

func TestDiscover_NoIssuerNoExplicit(t *testing.T) {
	_, err := Discover(context.Background(), "", "", "", "")
	if err == nil {
		t.Fatal("expected error when no issuer and no explicit URLs provided")
	}
}

func TestDiscover_IssuerMissingEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer srv.Close()

	_, err := Discover(context.Background(), srv.URL, "", "", "")
	if err == nil {
		t.Fatal("expected error when discovery returns empty endpoints")
	}
}

func TestDiscover_IssuerHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Discover(context.Background(), srv.URL, "", "", "")
	if err == nil {
		t.Fatal("expected error on HTTP error from issuer")
	}
}
