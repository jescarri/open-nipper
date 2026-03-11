package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

// AuthCodeFlowConfig holds the configuration for an authorization code flow
// with PKCE and a localhost redirect URI.
type AuthCodeFlowConfig struct {
	ClientID         string
	ClientSecret     string
	Scopes           []string
	AuthorizationURL string // from discovery or explicit config
	TokenURL         string // from discovery or explicit config
}

// authCodeResult is sent over a channel when the localhost callback fires.
type authCodeResult struct {
	Code  string
	State string
	Err   error
}

// pkceChallenge holds the PKCE verifier/challenge pair (RFC 7636).
type pkceChallenge struct {
	Verifier  string
	Challenge string
	Method    string // always "S256"
}

// generatePKCE creates a cryptographically random PKCE code verifier and
// its S256 code challenge.
func generatePKCE() (*pkceChallenge, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generating PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])
	return &pkceChallenge{
		Verifier:  verifier,
		Challenge: challenge,
		Method:    "S256",
	}, nil
}

// generateState creates a random state parameter for CSRF protection.
func generateState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// RunAuthCodeFlow performs the full OAuth 2.0 Authorization Code flow with PKCE.
//
// It spins up a temporary localhost HTTP server to capture the redirect,
// calls the notifier with the authorization URL for the user to open in a
// browser, waits for the callback, and exchanges the code for tokens.
//
// The flow times out after 5 minutes if the user does not authorize.
func RunAuthCodeFlow(ctx context.Context, endpoints *OIDCEndpoints, cfg *AuthCodeFlowConfig, serverName string, logger *zap.Logger, notifier DeviceAuthNotifier) (*StoredToken, error) {
	pkce, err := generatePKCE()
	if err != nil {
		return nil, err
	}
	state, err := generateState()
	if err != nil {
		return nil, err
	}

	// Pick an available port on localhost.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("binding localhost listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Build the authorization URL.
	authURL, err := buildAuthURL(endpoints.AuthorizationEndpoint, cfg, redirectURI, state, pkce)
	if err != nil {
		listener.Close()
		return nil, err
	}

	resultCh := make(chan authCodeResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			desc := q.Get("error_description")
			resultCh <- authCodeResult{Err: fmt.Errorf("authorization error: %s (%s)", errParam, desc)}
			fmt.Fprintf(w, "<html><body><h2>Authorization Failed</h2><p>%s: %s</p><p>You can close this window.</p></body></html>", errParam, desc)
			return
		}
		resultCh <- authCodeResult{
			Code:  q.Get("code"),
			State: q.Get("state"),
		}
		fmt.Fprint(w, "<html><body><h2>Authorization Successful</h2><p>You can close this window and return to the application.</p></body></html>")
	})

	srv := &http.Server{Handler: mux}

	// Start serving in the background.
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			resultCh <- authCodeResult{Err: fmt.Errorf("localhost server: %w", err)}
		}
	}()

	// Notify the user with the authorization URL.
	if notifier != nil {
		// Reuse the DeviceAuthNotifier signature: pass the auth URL as verificationURI,
		// "OPEN_IN_BROWSER" as the user code hint, and 300s (5 min) timeout.
		notifier(ctx, serverName, authURL, "OPEN_IN_BROWSER", 300)
	}
	logger.Info("authorization code flow started; waiting for user to authorize in browser",
		zap.String("server", serverName),
		zap.String("redirectURI", redirectURI),
	)

	// Wait for callback or timeout.
	const flowTimeout = 5 * time.Minute
	timeoutCtx, cancel := context.WithTimeout(ctx, flowTimeout)
	defer cancel()

	var result authCodeResult
	select {
	case result = <-resultCh:
	case <-timeoutCtx.Done():
		srv.Close()
		return nil, fmt.Errorf("authorization code flow timed out after %s", flowTimeout)
	}

	// Shut down the temporary server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)

	if result.Err != nil {
		return nil, result.Err
	}
	if result.State != state {
		return nil, fmt.Errorf("state mismatch: expected %q, got %q", state, result.State)
	}
	if result.Code == "" {
		return nil, fmt.Errorf("no authorization code received")
	}

	// Exchange the authorization code for tokens.
	return exchangeAuthCode(ctx, endpoints.TokenEndpoint, cfg, result.Code, redirectURI, pkce)
}

// buildAuthURL constructs the authorization endpoint URL with all required params.
func buildAuthURL(authEndpoint string, cfg *AuthCodeFlowConfig, redirectURI, state string, pkce *pkceChallenge) (string, error) {
	u, err := url.Parse(authEndpoint)
	if err != nil {
		return "", fmt.Errorf("parsing authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", pkce.Method)
	q.Set("access_type", "offline") // Google-specific: request refresh token
	q.Set("prompt", "consent")      // Google-specific: force consent to get refresh token
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// exchangeAuthCode exchanges the authorization code for access and refresh tokens.
func exchangeAuthCode(ctx context.Context, tokenEndpoint string, cfg *AuthCodeFlowConfig, code, redirectURI string, pkce *pkceChallenge) (*StoredToken, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {pkce.Verifier},
	}
	if cfg.ClientSecret != "" {
		data.Set("client_secret", cfg.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging authorization code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token exchange failed: %s", resp.Status)
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}
	if tokenResp.Error != "" {
		return nil, fmt.Errorf("token endpoint error: %s", tokenResp.Error)
	}

	token := &StoredToken{
		AccessToken:  tokenResp.AccessToken,
		TokenType:    tokenResp.TokenType,
		RefreshToken: tokenResp.RefreshToken,
	}
	if tokenResp.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return token, nil
}
