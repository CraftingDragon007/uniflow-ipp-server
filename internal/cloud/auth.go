package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type DeviceAuthorizationResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type TokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
}

type AuthConfig struct {
	ClientID       string
	ClientSecret   string
	Scope          string
	DeviceEndpoint string
	TokenEndpoint  string
	TokenPath      string
	HTTPClient     *http.Client
}

type TokenManager struct {
	cfg   AuthConfig
	mu    sync.Mutex
	token *TokenResponse
}

func NewTokenManager(cfg AuthConfig) *TokenManager {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &TokenManager{cfg: cfg}
}

func (m *TokenManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.cfg.TokenPath)
	if err != nil {
		return err
	}
	var tok TokenResponse
	if err := json.Unmarshal(data, &tok); err != nil {
		return fmt.Errorf("parse token: %w", err)
	}
	if err := validateToken(&tok); err != nil {
		return err
	}
	// Older files without an absolute expiration must be refreshed, not
	// treated as indefinitely valid after every daemon restart.
	m.token = &tok
	return nil
}

func validateToken(tok *TokenResponse) error {
	if tok.AccessToken == "" || !strings.EqualFold(tok.TokenType, "Bearer") {
		return fmt.Errorf("token requires a nonempty access_token and Bearer token_type")
	}
	return nil
}

func (m *TokenManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *TokenManager) saveLocked() error {
	if m.token == nil {
		return fmt.Errorf("no token to save")
	}
	data, err := json.MarshalIndent(m.token, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.cfg.TokenPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), m.cfg.TokenPath)
}

func (m *TokenManager) AccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == nil {
		return "", fmt.Errorf("no access token; provision tenant credentials first")
	}
	if time.Now().Add(30 * time.Second).Before(m.token.ExpiresAt) {
		return m.token.AccessToken, nil
	}
	if m.token.RefreshToken == "" || m.cfg.TokenEndpoint == "" || m.cfg.ClientID == "" {
		return "", fmt.Errorf("access token expired or expiration unknown; refresh credentials are missing")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {m.cfg.ClientID},
		"refresh_token": {m.token.RefreshToken},
	}
	if m.cfg.ClientSecret != "" {
		form.Set("client_secret", m.cfg.ClientSecret)
	}
	if m.cfg.Scope != "" {
		form.Set("scope", m.cfg.Scope)
	}
	var tr TokenResponse
	if err := m.postForm(ctx, m.cfg.TokenEndpoint, form, &tr); err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	if tr.RefreshToken == "" {
		tr.RefreshToken = m.token.RefreshToken
	}
	if err := m.storeLocked(&tr); err != nil {
		return "", err
	}
	return tr.AccessToken, nil
}

func (m *TokenManager) storeLocked(tok *TokenResponse) error {
	if err := validateToken(tok); err != nil {
		return err
	}
	if tok.ExpiresIn <= 0 {
		return fmt.Errorf("token response requires positive expires_in")
	}
	tok.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	old := m.token
	m.token = tok
	if err := m.saveLocked(); err != nil {
		m.token = old
		return fmt.Errorf("persist token: %w", err)
	}
	return nil
}

type OAuthError struct {
	Code string `json:"error"`
}

func (e *OAuthError) Error() string { return "OAuth error: " + e.Code }

func (m *TokenManager) postForm(ctx context.Context, endpoint string, form url.Values, result interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-ClientType", "SmartClientForMac")
	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var oauthErr OAuthError
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&oauthErr); err != nil {
			return fmt.Errorf("OAuth endpoint returned HTTP %d (invalid error response)", resp.StatusCode)
		}
		if oauthErr.Code == "" {
			return fmt.Errorf("OAuth endpoint returned HTTP %d", resp.StatusCode)
		}
		return &oauthErr
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return fmt.Errorf("decode OAuth response: %w", err)
	}
	return nil
}

// Device flow is opt-in for providers supporting RFC 8628; it is not the
// SmartClient's peer-registration flow.
func (m *TokenManager) BeginDeviceAuthorization(ctx context.Context) (*DeviceAuthorizationResponse, error) {
	if m.cfg.DeviceEndpoint == "" || m.cfg.TokenEndpoint == "" || m.cfg.ClientID == "" || m.cfg.Scope == "" {
		return nil, fmt.Errorf("device authorization requires explicit endpoints, client ID and scope")
	}
	var dar DeviceAuthorizationResponse
	if err := m.postForm(ctx, m.cfg.DeviceEndpoint, url.Values{
		"client_id": {m.cfg.ClientID},
		"scope":     {m.cfg.Scope},
	}, &dar); err != nil {
		return nil, err
	}
	if dar.DeviceCode == "" || dar.UserCode == "" || dar.VerificationURI == "" || dar.ExpiresIn <= 0 || dar.Interval < 0 {
		return nil, fmt.Errorf("invalid device authorization response")
	}
	return &dar, nil
}

func (m *TokenManager) PollForToken(ctx context.Context, dar *DeviceAuthorizationResponse) (*TokenResponse, error) {
	if dar == nil || dar.DeviceCode == "" || dar.ExpiresIn <= 0 || dar.Interval < 0 {
		return nil, fmt.Errorf("invalid device authorization")
	}
	interval := time.Duration(dar.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(dar.ExpiresIn)*time.Second)
	defer cancel()
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		var tr TokenResponse
		err := m.postForm(ctx, m.cfg.TokenEndpoint, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {m.cfg.ClientID},
			"device_code": {dar.DeviceCode},
		}, &tr)
		if oauthErr, ok := err.(*OAuthError); ok {
			switch oauthErr.Code {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		err = m.storeLocked(&tr)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &tr, nil
	}
}
