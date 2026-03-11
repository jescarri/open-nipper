package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

// DeviceAuthResponse represents the response from the device authorization endpoint (RFC 8628).
// Google uses the non-standard "verification_url" instead of "verification_uri",
// so we decode both and normalize in RequestDeviceCode.
type DeviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURL         string `json:"verification_url,omitempty"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceFlowConfig holds the configuration for initiating a device authorization flow.
type DeviceFlowConfig struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	Audience     string
}

// tokenResponse represents the JSON response from the token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// pollResult is used internally to communicate polling state.
type pollResult struct {
	token         *StoredToken
	err           error
	increaseDelay bool
}

// RequestDeviceCode initiates the device authorization flow by POSTing to the device_authorization_endpoint.
func RequestDeviceCode(ctx context.Context, endpoints *OIDCEndpoints, cfg *DeviceFlowConfig) (*DeviceAuthResponse, error) {
	data := url.Values{}
	data.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		data.Set("client_secret", cfg.ClientSecret)
	}
	if len(cfg.Scopes) > 0 {
		data.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if cfg.Audience != "" {
		data.Set("audience", cfg.Audience)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.DeviceAuthorizationEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating device auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization endpoint returned status %d", resp.StatusCode)
	}

	var deviceResp DeviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&deviceResp); err != nil {
		return nil, fmt.Errorf("decoding device auth response: %w", err)
	}

	// Google returns "verification_url" instead of the RFC 8628 "verification_uri".
	if deviceResp.VerificationURI == "" && deviceResp.VerificationURL != "" {
		deviceResp.VerificationURI = deviceResp.VerificationURL
	}

	return &deviceResp, nil
}

// PollForToken polls the token endpoint until the user authorizes or the code expires.
// It logs the verification URI and user code via the provided logger.
// Returns a StoredToken on success.
func PollForToken(ctx context.Context, endpoints *OIDCEndpoints, cfg *DeviceFlowConfig, deviceResp *DeviceAuthResponse, logger *zap.Logger) (*StoredToken, error) {
	interval := deviceResp.Interval
	if interval < 5 {
		interval = 5
	}

	logger.Info("OIDC device authorization flow started; waiting for user to authorize")

	deadline := time.Now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("device code expired")
		}

		result := exchangeDeviceCode(ctx, endpoints, cfg, deviceResp.DeviceCode)
		if result.err != nil {
			return nil, result.err
		}
		if result.increaseDelay {
			interval += 5
			continue
		}
		if result.token != nil {
			return result.token, nil
		}
		// authorization_pending: continue polling
	}
}

func exchangeDeviceCode(ctx context.Context, endpoints *OIDCEndpoints, cfg *DeviceFlowConfig, deviceCode string) pollResult {
	data := url.Values{}
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	data.Set("device_code", deviceCode)
	data.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		data.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return pollResult{err: fmt.Errorf("creating token request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return pollResult{err: fmt.Errorf("polling token endpoint: %w", err)}
	}
	defer resp.Body.Close()

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return pollResult{err: fmt.Errorf("decoding token response: %w", err)}
	}

	switch tokenResp.Error {
	case "":
		return pollResult{
			token: &StoredToken{
				AccessToken:  tokenResp.AccessToken,
				TokenType:    tokenResp.TokenType,
				RefreshToken: tokenResp.RefreshToken,
				Expiry:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
			},
		}
	case "authorization_pending":
		return pollResult{}
	case "slow_down":
		return pollResult{increaseDelay: true}
	case "expired_token":
		return pollResult{err: fmt.Errorf("device code expired")}
	case "access_denied":
		return pollResult{err: fmt.Errorf("access denied by user")}
	default:
		return pollResult{err: fmt.Errorf("token endpoint error: %s", tokenResp.Error)}
	}
}
