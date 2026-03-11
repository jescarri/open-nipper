package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OIDCEndpoints holds the endpoints discovered from OIDC well-known configuration.
type OIDCEndpoints struct {
	AuthorizationEndpoint       string
	DeviceAuthorizationEndpoint string
	TokenEndpoint               string
}

// wellKnownResponse represents the relevant fields from the OIDC discovery document.
type wellKnownResponse struct {
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

// Discover fetches OIDC well-known configuration and extracts endpoints.
// If issuerURL is provided, fetches from {issuerURL}/.well-known/openid-configuration.
// Falls back to explicit overrides from authorizationURL/deviceAuthURL/tokenURL.
//
// For the authorization_code flow, authorizationURL and tokenURL are required.
// For the device flow, deviceAuthURL and tokenURL are required.
// The caller decides which flow to use; Discover populates all available endpoints.
func Discover(ctx context.Context, issuerURL, authorizationURL, deviceAuthURL, tokenURL string) (*OIDCEndpoints, error) {
	endpoints := &OIDCEndpoints{
		AuthorizationEndpoint:       authorizationURL,
		DeviceAuthorizationEndpoint: deviceAuthURL,
		TokenEndpoint:               tokenURL,
	}

	if issuerURL != "" {
		discovered, err := fetchWellKnown(ctx, issuerURL)
		if err != nil {
			return nil, fmt.Errorf("oidc discovery from %s: %w", issuerURL, err)
		}
		// Only use discovered values if explicit overrides are not set.
		if endpoints.AuthorizationEndpoint == "" {
			endpoints.AuthorizationEndpoint = discovered.AuthorizationEndpoint
		}
		if endpoints.DeviceAuthorizationEndpoint == "" {
			endpoints.DeviceAuthorizationEndpoint = discovered.DeviceAuthorizationEndpoint
		}
		if endpoints.TokenEndpoint == "" {
			endpoints.TokenEndpoint = discovered.TokenEndpoint
		}
	}

	if endpoints.TokenEndpoint == "" {
		return nil, fmt.Errorf("token_endpoint not found and no explicit override provided")
	}

	return endpoints, nil
}

func fetchWellKnown(ctx context.Context, issuerURL string) (*wellKnownResponse, error) {
	url := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching well-known config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("well-known config returned status %d", resp.StatusCode)
	}

	var wk wellKnownResponse
	if err := json.NewDecoder(resp.Body).Decode(&wk); err != nil {
		return nil, fmt.Errorf("decoding well-known config: %w", err)
	}

	return &wk, nil
}
