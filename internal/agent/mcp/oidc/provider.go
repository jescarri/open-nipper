// Package oidc implements OAuth 2.0 flows for MCP clients:
//   - Device Authorization Grant (RFC 8628) for headless environments
//   - Authorization Code with PKCE (RFC 7636) for broader scope access
//
// Tokens are encrypted at rest with AES-256-GCM + Argon2id key derivation.
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
	"golang.org/x/oauth2"
)

// DeviceAuthNotifier is called when the device authorization flow starts.
// It receives the verification URL, user code, and expiry so the caller
// can notify the user through available channels (WhatsApp, Slack, etc.)
// instead of relying on container logs.
type DeviceAuthNotifier func(ctx context.Context, serverName, verificationURI, userCode string, expiresIn int)

// Provider manages the OIDC lifecycle for a single MCP server.
// It handles discovery, device flow, token persistence, and refresh.
type Provider struct {
	cfg                *ProviderConfig
	store              *EncryptedFileTokenStore
	endpoints          *OIDCEndpoints
	logger             *zap.Logger
	httpClient         *http.Client
	encryptionPassword string
	serverName         string
	notifier           DeviceAuthNotifier
}

// ProviderConfig holds the OIDC configuration for a Provider.
type ProviderConfig struct {
	ClientID         string
	ClientSecret     string
	Scopes           []string
	IssuerURL        string
	AuthorizationURL string // explicit override for authorization_endpoint
	DeviceAuthURL    string
	TokenURL         string
	Audience         string
	// Flow selects the OAuth flow: "device" (default) or "authorization_code".
	Flow string
}

// NewProvider creates a Provider. Discovers OIDC endpoints eagerly.
// notifier is optional; when non-nil, it is called with the verification URL and
// user code when the device authorization flow starts.
func NewProvider(ctx context.Context, cfg *ProviderConfig, basePath, serverName, encryptionPassword string, logger *zap.Logger, notifier DeviceAuthNotifier) (*Provider, error) {
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("client_id is required for OIDC auth")
	}
	if cfg.IssuerURL == "" && cfg.TokenURL == "" {
		return nil, fmt.Errorf("either issuer_url or token_url is required")
	}

	endpoints, err := Discover(ctx, cfg.IssuerURL, cfg.AuthorizationURL, cfg.DeviceAuthURL, cfg.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}

	store := NewEncryptedFileTokenStore(basePath, serverName, encryptionPassword)

	return &Provider{
		cfg:                cfg,
		store:              store,
		endpoints:          endpoints,
		logger:             logger,
		httpClient:         http.DefaultClient,
		encryptionPassword: encryptionPassword,
		serverName:         serverName,
		notifier:           notifier,
	}, nil
}

// EnsureToken guarantees a valid token exists in the store.
// 1. Tries to load from store.
// 2. If valid, returns nil.
// 3. If expired with refresh_token, attempts refresh.
// 4. If no token or refresh fails, runs Device Authorization Grant flow.
func (p *Provider) EnsureToken(ctx context.Context) error {
	token, err := p.store.GetToken()
	if err != nil {
		return fmt.Errorf("reading stored token: %w", err)
	}

	if token != nil && !token.Expiry.IsZero() && token.Expiry.After(time.Now()) {
		p.logger.Info("using cached OIDC token", zap.Time("expiry", token.Expiry))
		return nil
	}

	// Try refresh if we have a refresh token.
	if token != nil && token.RefreshToken != "" {
		p.logger.Info("OIDC access token expired, attempting refresh")
		refreshed, err := p.refreshToken(ctx, token)
		if err == nil {
			if saveErr := p.store.SaveToken(refreshed); saveErr != nil {
				return fmt.Errorf("saving refreshed token: %w", saveErr)
			}
			p.logger.Info("OIDC token refreshed", zap.Time("expiry", refreshed.Expiry))
			return nil
		}
		p.logger.Warn("OIDC token refresh failed, falling back to interactive flow", zap.Error(err))
	}

	// Dispatch to the appropriate OAuth flow.
	var newToken *StoredToken
	switch strings.ToLower(p.cfg.Flow) {
	case "authorization_code":
		newToken, err = p.runAuthCodeFlow(ctx)
	default:
		newToken, err = p.runDeviceFlow(ctx)
	}
	if err != nil {
		return err
	}

	if saveErr := p.store.SaveToken(newToken); saveErr != nil {
		return fmt.Errorf("saving new token: %w", saveErr)
	}

	p.logger.Info("OIDC token acquired and saved", zap.Time("expiry", newToken.Expiry))
	return nil
}

// runDeviceFlow executes the RFC 8628 Device Authorization Grant flow.
func (p *Provider) runDeviceFlow(ctx context.Context) (*StoredToken, error) {
	if p.endpoints.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("device_authorization_endpoint not available; configure device_auth_url or use authorization_code flow")
	}

	p.logger.Info("starting OIDC device authorization flow")
	flowCfg := &DeviceFlowConfig{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Scopes:       p.cfg.Scopes,
		Audience:     p.cfg.Audience,
	}

	deviceResp, err := RequestDeviceCode(ctx, p.endpoints, flowCfg)
	if err != nil {
		return nil, fmt.Errorf("requesting device code: %w", err)
	}

	// Prefer verification_uri_complete (includes code in URL); fall back to plain URI.
	verifyURL := deviceResp.VerificationURIComplete
	if verifyURL == "" {
		verifyURL = deviceResp.VerificationURI
	}

	// Notify the user through available channels (WhatsApp, Slack, etc.).
	if p.notifier != nil {
		p.notifier(ctx, p.serverName, verifyURL, deviceResp.UserCode, deviceResp.ExpiresIn)
	}

	p.logger.Info("OIDC device authorization started; notification sent to user channels",
		zap.String("server", p.serverName),
		zap.Int("expiresIn", deviceResp.ExpiresIn),
	)

	return PollForToken(ctx, p.endpoints, flowCfg, deviceResp, p.logger)
}

// runAuthCodeFlow executes the OAuth 2.0 Authorization Code flow with PKCE
// using a temporary localhost redirect server.
func (p *Provider) runAuthCodeFlow(ctx context.Context) (*StoredToken, error) {
	if p.endpoints.AuthorizationEndpoint == "" {
		return nil, fmt.Errorf("authorization_endpoint not available; configure authorization_url or check issuer discovery")
	}

	p.logger.Info("starting OIDC authorization code flow with PKCE")
	flowCfg := &AuthCodeFlowConfig{
		ClientID:         p.cfg.ClientID,
		ClientSecret:     p.cfg.ClientSecret,
		Scopes:           p.cfg.Scopes,
		AuthorizationURL: p.endpoints.AuthorizationEndpoint,
		TokenURL:         p.endpoints.TokenEndpoint,
	}

	return RunAuthCodeFlow(ctx, p.endpoints, flowCfg, p.serverName, p.logger, p.notifier)
}

// TokenSource returns an oauth2.TokenSource that reads from the encrypted store
// and auto-refreshes expired tokens. Suitable for use with http.Client.
func (p *Provider) TokenSource() oauth2.TokenSource {
	return &providerTokenSource{provider: p}
}

// OAuthRoundTripper returns an http.RoundTripper that injects the Bearer token
// from the store into every request. If the token is expired and a refresh
// token is available, it refreshes automatically.
func (p *Provider) OAuthRoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &oauth2.Transport{
		Source: p.TokenSource(),
		Base:   base,
	}
}

// refreshToken exchanges a refresh token for a new access token.
func (p *Provider) refreshToken(ctx context.Context, stored *StoredToken) (*StoredToken, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {stored.RefreshToken},
		"client_id":     {p.cfg.ClientID},
	}
	if p.cfg.ClientSecret != "" {
		data.Set("client_secret", p.cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.endpoints.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token refresh failed: %s", resp.Status)
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding refresh response: %w", err)
	}

	result := &StoredToken{
		AccessToken: tokenResp.AccessToken,
		TokenType:   tokenResp.TokenType,
		// Keep existing refresh token if new one not provided.
		RefreshToken: stored.RefreshToken,
	}
	if tokenResp.RefreshToken != "" {
		result.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		result.Expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return result, nil
}

// providerTokenSource implements oauth2.TokenSource using the Provider's store.
type providerTokenSource struct {
	provider *Provider
}

func (ts *providerTokenSource) Token() (*oauth2.Token, error) {
	stored, err := ts.provider.store.GetToken()
	if err != nil {
		return nil, fmt.Errorf("reading token from store: %w", err)
	}
	if stored == nil {
		return nil, fmt.Errorf("no OIDC token available; run device authorization flow first")
	}

	// If token is expired and we have a refresh token, try refreshing.
	if !stored.Expiry.IsZero() && stored.Expiry.Before(time.Now()) && stored.RefreshToken != "" {
		refreshed, err := ts.provider.refreshToken(context.Background(), stored)
		if err != nil {
			return nil, fmt.Errorf("token refresh failed: %w", err)
		}
		if saveErr := ts.provider.store.SaveToken(refreshed); saveErr != nil {
			return nil, fmt.Errorf("saving refreshed token: %w", saveErr)
		}
		stored = refreshed
	}

	return &oauth2.Token{
		AccessToken:  stored.AccessToken,
		TokenType:    stored.TokenType,
		RefreshToken: stored.RefreshToken,
		Expiry:       stored.Expiry,
	}, nil
}
