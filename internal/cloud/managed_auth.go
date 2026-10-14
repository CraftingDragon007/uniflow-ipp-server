package cloud

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const renewalWindow = 7 * 24 * time.Hour
const maxAuthResponse = 1 << 20

type PeerCredentials struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	Scope        string    `json:"scope"`
	TokenURI     string    `json:"token_uri"`
	RedirectURI  string    `json:"redirect_uri"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type AuthState struct {
	Version              int             `json:"version"`
	TenantDomain         string          `json:"tenant_domain"`
	UserID               string          `json:"user_id"`
	Peer                 PeerCredentials `json:"peer"`
	Token                TokenResponse   `json:"token"`
	RenewalPrivateKeyPEM string          `json:"renewal_private_key_pem"`
}

type ManagedAuth struct {
	submissionMu sync.Mutex
	mu           sync.Mutex
	path         string
	tenant       string
	userID       string
	management   string
	httpClient   *http.Client
	state        AuthState
	key          *rsa.PrivateKey
}

// BeginSubmission prevents credential rotation while a cloud upload is in flight.
func (m *ManagedAuth) BeginSubmission(ctx context.Context) (string, func(), error) {
	m.submissionMu.Lock()
	token, err := m.AccessToken(ctx)
	if err != nil {
		m.submissionMu.Unlock()
		return "", nil, err
	}
	return token, m.submissionMu.Unlock, nil
}

func NewManagedAuth(path, tenant, userID, management string, client *http.Client) *ManagedAuth {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ManagedAuth{path: path, tenant: tenant, userID: userID, management: management, httpClient: &copyClient}
}

func (m *ManagedAuth) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, err := os.Lstat(m.path)
	if err != nil {
		return fmt.Errorf("read auth state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("auth state must be a private regular file of at most 2 MiB")
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return fmt.Errorf("read auth state: %w", err)
	}
	var state AuthState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse auth state: %w", err)
	}
	key, err := validateAuthState(&state, m.tenant, m.userID)
	if err != nil {
		return err
	}
	m.state, m.key = state, key
	return nil
}

func validateAuthState(state *AuthState, tenant, userID string) (*rsa.PrivateKey, error) {
	if state.Version != 2 || !strings.EqualFold(state.TenantDomain, tenant) || !sameUserID(state.UserID, userID) {
		return nil, fmt.Errorf("auth state version, tenant, or user does not match configuration")
	}
	p := state.Peer
	if p.ClientID == "" || p.ClientSecret == "" || p.Scope == "" || !validRedirectURI(p.RedirectURI) || p.ExpiresAt.IsZero() || !validHTTPS(p.TokenURI) {
		return nil, fmt.Errorf("auth state has incomplete peer credentials")
	}
	if err := validateToken(&state.Token); err != nil || state.Token.RefreshToken == "" || state.Token.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("auth state has incomplete OAuth tokens")
	}
	block, _ := pem.Decode([]byte(state.RenewalPrivateKeyPEM))
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("auth state has no supported renewal key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, ok := parsed.(*rsa.PrivateKey)
	if err != nil || !ok || key.N.BitLen() != 2048 || key.Validate() != nil {
		return nil, fmt.Errorf("auth state has an invalid renewal key")
	}
	return key, nil
}

func validHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasSuffix(host, ".localhost") && net.ParseIP(host) == nil
}

func validRedirectURI(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme != ""
}

func sameUserID(a, b string) bool {
	a = strings.ReplaceAll(strings.ToLower(a), "-", "")
	b = strings.ReplaceAll(strings.ToLower(b), "-", "")
	return len(a) == 32 && a == b
}

func (m *ManagedAuth) AccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.key == nil {
		return "", fmt.Errorf("auth state has not been loaded")
	}
	if !time.Now().Before(m.state.Peer.ExpiresAt) {
		if err := m.renewLocked(ctx); err != nil {
			return "", fmt.Errorf("peer credentials expired; sign in again if renewal keeps failing: %w", err)
		}
	}
	return m.accessTokenLocked(ctx)
}

func (m *ManagedAuth) accessTokenLocked(ctx context.Context) (string, error) {
	if time.Now().Add(30 * time.Second).Before(m.state.Token.ExpiresAt) {
		return m.state.Token.AccessToken, nil
	}
	peer := m.state.Peer
	form := url.Values{
		"grant_type": {"refresh_token"}, "client_id": {peer.ClientID},
		"client_secret": {peer.ClientSecret}, "scope": {peer.Scope},
		"refresh_token": {m.state.Token.RefreshToken},
	}
	tr, err := m.postToken(ctx, peer.TokenURI, form)
	if err != nil {
		return "", fmt.Errorf("token refresh: %w", err)
	}
	if tr.RefreshToken == "" {
		tr.RefreshToken = m.state.Token.RefreshToken
	}
	if err := validateNewToken(&tr); err != nil {
		return "", err
	}
	tr.Scope = peer.Scope
	tr.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	next := m.state
	next.Token = tr
	if err := m.persistLocked(&next); err != nil {
		return "", err
	}
	return tr.AccessToken, nil
}

func validateNewToken(tok *TokenResponse) error {
	if tok.AccessToken == "" || tok.ExpiresIn <= 0 || tok.ExpiresIn > 31_536_000 {
		return fmt.Errorf("OAuth response contains an invalid access token or expiration")
	}
	for _, b := range []byte(tok.AccessToken) {
		if b < 33 || b > 126 {
			return fmt.Errorf("OAuth response contains invalid access token characters")
		}
	}
	// The native SmartClient uses a Bearer header regardless of token_type metadata.
	tok.TokenType = "Bearer"
	return nil
}

func (m *ManagedAuth) postToken(ctx context.Context, endpoint string, form url.Values) (TokenResponse, error) {
	var tr TokenResponse
	if !validHTTPS(endpoint) {
		return tr, fmt.Errorf("OAuth token endpoint is not HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tr, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-ClientType", "SmartClientForMac")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return tr, fmt.Errorf("OAuth connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tr, fmt.Errorf("OAuth endpoint returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthResponse+1))
	if err != nil || len(data) > maxAuthResponse {
		return tr, fmt.Errorf("OAuth response could not be read within size limit")
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return tr, fmt.Errorf("OAuth response is invalid JSON")
	}
	return tr, nil
}

type renewalResponse struct {
	Key         string `json:"Key"`
	Data        string `json:"Data"`
	TokenURI    string `json:"TokenUri"`
	RedirectURI string `json:"RedirectUri"`
	Code        string `json:"Code"`
	UserID      string `json:"UserId"`
}

func (m *ManagedAuth) RenewIfDue(ctx context.Context) error {
	m.submissionMu.Lock()
	defer m.submissionMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.key == nil {
		return fmt.Errorf("auth state has not been loaded")
	}
	if time.Now().Add(renewalWindow).Before(m.state.Peer.ExpiresAt) {
		return nil
	}
	return m.renewLocked(ctx)
}

func (m *ManagedAuth) renewLocked(ctx context.Context) error {
	token, err := m.accessTokenLocked(ctx)
	if err != nil {
		return err
	}
	endpoint := "https://" + m.management + "/api/v1/registration/newcredentials"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build renewal request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("credential renewal connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("credential renewal returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthResponse+1))
	if err != nil || len(data) > maxAuthResponse {
		return fmt.Errorf("credential renewal response exceeds size limit or could not be read")
	}
	var response renewalResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("credential renewal response is invalid JSON")
	}
	peer, err := m.decodeRenewal(&response)
	if err != nil {
		return err
	}
	tr, err := m.postToken(ctx, peer.TokenURI, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {peer.ClientID},
		"client_secret": {peer.ClientSecret}, "scope": {peer.Scope},
		"redirect_uri": {peer.RedirectURI}, "code": {response.Code},
	})
	if err != nil {
		return fmt.Errorf("renewed credential token exchange: %w", err)
	}
	if err := validateNewToken(&tr); err != nil || tr.RefreshToken == "" {
		return fmt.Errorf("renewed credentials did not provide usable OAuth tokens")
	}
	tr.Scope = peer.Scope
	tr.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	next := m.state
	next.Peer, next.Token = peer, tr
	if err := m.persistLocked(&next); err != nil {
		return err
	}
	log.Print("peer credentials renewed and private auth state saved")
	return nil
}

func (m *ManagedAuth) decodeRenewal(response *renewalResponse) (PeerCredentials, error) {
	var peer PeerCredentials
	if response.Key == "" || response.Data == "" || response.Code == "" || !validHTTPS(response.TokenURI) || !validRedirectURI(response.RedirectURI) {
		return peer, fmt.Errorf("credential renewal response is incomplete")
	}
	if response.UserID != "" && !sameUserID(response.UserID, m.state.UserID) {
		return peer, fmt.Errorf("credential renewal returned a different user")
	}
	wrapped, err := base64.StdEncoding.Strict().DecodeString(response.Key)
	if err != nil {
		return peer, fmt.Errorf("credential renewal key is invalid")
	}
	secret, err := rsa.DecryptPKCS1v15(rand.Reader, m.key, wrapped)
	if err != nil {
		return peer, fmt.Errorf("credential renewal key cannot be decrypted")
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return peer, fmt.Errorf("credential renewal cipher is invalid")
	}
	encoded, err := base64.StdEncoding.Strict().DecodeString(response.Data)
	if err != nil || len(encoded) == 0 || len(encoded)%block.BlockSize() != 0 {
		return peer, fmt.Errorf("credential renewal data is invalid")
	}
	plain := make([]byte, len(encoded))
	cipher.NewCBCDecrypter(block, make([]byte, block.BlockSize())).CryptBlocks(plain, encoded)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > block.BlockSize() || pad > len(plain) {
		return peer, fmt.Errorf("credential renewal padding is invalid")
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return peer, fmt.Errorf("credential renewal padding is invalid")
		}
	}
	plain = plain[:len(plain)-pad]
	plain = []byte(strings.TrimRight(strings.TrimPrefix(string(plain), "\ufeff"), "\x00"))
	if !utf8.Valid(plain) {
		return peer, fmt.Errorf("credential renewal data is not UTF-8")
	}
	parts := strings.Split(string(plain), "&")
	if len(parts) < 4 {
		return peer, fmt.Errorf("credential renewal data is incomplete")
	}
	values := make([]string, 4)
	for i := range values {
		values[i], err = url.QueryUnescape(parts[i])
		if err != nil {
			return peer, fmt.Errorf("credential renewal data cannot be decoded")
		}
	}
	ticks, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || ticks < 116444736000000000 {
		return peer, fmt.Errorf("credential renewal expiration is invalid")
	}
	seconds := ticks/10_000_000 - 11_644_473_600
	nanos := ticks % 10_000_000 * 100
	peer = PeerCredentials{ClientID: values[1], ClientSecret: values[2], Scope: values[3], TokenURI: response.TokenURI, RedirectURI: response.RedirectURI, ExpiresAt: time.Unix(seconds, nanos).UTC()}
	if peer.ClientID == "" || peer.ClientSecret == "" || peer.Scope == "" || !peer.ExpiresAt.After(time.Now()) {
		return PeerCredentials{}, fmt.Errorf("credential renewal returned empty or expired peer credentials")
	}
	return peer, nil
}

func (m *ManagedAuth) persistLocked(next *AuthState) error {
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth state: %w", err)
	}
	if info, err := os.Lstat(m.path); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("auth state destination is not a regular file")
	}
	dir := filepath.Dir(m.path)
	f, err := os.CreateTemp(dir, ".auth-state-*")
	if err != nil {
		return fmt.Errorf("create auth state replacement: %w", err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
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
	if err := os.Rename(f.Name(), m.path); err != nil {
		return fmt.Errorf("replace auth state: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	m.state = *next
	return nil
}

// Run checks even when no jobs arrive. A failure never discards the previous state.
func (m *ManagedAuth) Run(ctx context.Context) {
	delay := time.Duration(0)
	backoff := 15 * time.Minute
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := m.RenewIfDue(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("peer credential renewal failed: %v; will retry", err)
			}
			delay = backoff
			backoff *= 2
			if backoff > 6*time.Hour {
				backoff = 6 * time.Hour
			}
		} else {
			delay = time.Hour
			backoff = 15 * time.Minute
		}
	}
}
