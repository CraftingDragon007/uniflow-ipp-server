package cloud

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testUserID = "12345678-90ab-cdef-1234-567890abcdef"

func managedFixture(t *testing.T, peerExpiry time.Time) (*ManagedAuth, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	state := AuthState{
		Version: 2, TenantDomain: "tenant.example.invalid", UserID: testUserID,
		Peer:                 PeerCredentials{ClientID: "old-client", ClientSecret: "old-secret", Scope: "old-scope", TokenURI: "https://oauth.example.invalid/token", RedirectURI: "https://tenant.example.invalid/callback", ExpiresAt: peerExpiry},
		Token:                TokenResponse{AccessToken: "old-access", RefreshToken: "old-refresh", TokenType: "Bearer", ExpiresIn: 3600, ExpiresAt: time.Now().Add(time.Hour)},
		RenewalPrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	}
	path := filepath.Join(t.TempDir(), "auth-state.json")
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManagedAuth(path, state.TenantDomain, state.UserID, "tenant.example.invalid", nil)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	return m, key, path
}

func encryptedRenewal(t *testing.T, key *rsa.PrivateKey, user string) renewalResponse {
	t.Helper()
	aesKey := bytes.Repeat([]byte{7}, 32)
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, &key.PublicKey, aesKey)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(60 * 24 * time.Hour)
	ticks := (expiry.Unix()+11_644_473_600)*10_000_000 + int64(expiry.Nanosecond()/100)
	plain := []byte(fmt.Sprintf("%d&new-client&new-secret&new-scope", ticks))
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(ciphertext, plain)
	return renewalResponse{
		Key: base64.StdEncoding.EncodeToString(wrapped), Data: base64.StdEncoding.EncodeToString(ciphertext),
		TokenURI: "https://oauth.example.invalid/token", RedirectURI: "https://tenant.example.invalid/callback", Code: "new-code", UserID: user,
	}
}

func TestManagedRenewalAndRestart(t *testing.T) {
	m, key, path := managedFixture(t, time.Now().Add(24*time.Hour))
	response := encryptedRenewal(t, key, testUserID)
	requests := 0
	m.httpClient.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/v1/registration/newcredentials":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer old-access" {
				t.Errorf("wrong renewal request: %s %s", r.Method, r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(response)
		case "/token":
			_ = r.ParseForm()
			if r.Method != http.MethodPost || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_secret") != "new-secret" || r.Form.Get("code") != "new-code" || r.Header.Get("X-ClientType") != "SmartClientForMac" {
				t.Errorf("wrong OAuth exchange: %s %v", r.Method, r.Form)
			}
			_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
		default:
			t.Errorf("unexpected URL: %s", r.URL.Path)
		}
	})}
	if err := m.RenewIfDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	got, err := m.AccessToken(context.Background())
	if err != nil || got != "new-access" {
		t.Fatalf("access token: %q, %v", got, err)
	}
	reloaded := NewManagedAuth(path, "tenant.example.invalid", testUserID, "tenant.example.invalid", nil)
	if err := reloaded.Load(); err != nil || reloaded.state.Peer.ClientSecret != "new-secret" || reloaded.state.Token.RefreshToken != "new-refresh" {
		t.Fatalf("persisted state was not renewed: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("auth state permissions: %v, %v", info, err)
	}
}

func TestManagedRenewalFailurePreservesState(t *testing.T) {
	for _, scenario := range []string{"wrong-user", "token-failure", "invalid-cipher"} {
		t.Run(scenario, func(t *testing.T) {
			m, key, path := managedFixture(t, time.Now().Add(24*time.Hour))
			before, _ := os.ReadFile(path)
			response := encryptedRenewal(t, key, testUserID)
			if scenario == "wrong-user" {
				response.UserID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			} else if scenario == "invalid-cipher" {
				response.Data = "bad"
			}
			m.httpClient.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error_description":"private-value"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(response)
			})}
			err := m.RenewIfDue(context.Background())
			if err == nil || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("renewal failure was not sanitized: %v", err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || m.state.Peer.ClientSecret != "old-secret" {
				t.Fatal("failed renewal changed auth state")
			}
		})
	}
}

func TestManagedTokenRefreshAndIdentityValidation(t *testing.T) {
	m, _, path := managedFixture(t, time.Now().Add(30*24*time.Hour))
	m.state.Token.ExpiresAt = time.Now().Add(-time.Minute)
	if err := m.persistLocked(&m.state); err != nil {
		t.Fatal(err)
	}
	m.httpClient.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Errorf("wrong refresh form: %v", r.Form)
		}
		_, _ = w.Write([]byte(`{"access_token":"refreshed","expires_in":"3600","token_type":"JWT"}`))
	})}
	got, err := m.AccessToken(context.Background())
	if err != nil || got != "refreshed" {
		t.Fatalf("refresh: %q, %v", got, err)
	}
	var state AuthState
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &state); err != nil || state.Token.TokenType != "Bearer" || state.Token.RefreshToken != "old-refresh" {
		t.Fatalf("refresh persistence failed: %v", err)
	}
	wrongUser := NewManagedAuth(path, "tenant.example.invalid", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "tenant.example.invalid", nil)
	if err := wrongUser.Load(); err == nil {
		t.Fatal("different configured user accepted")
	}
}

func TestRenewalWaitsForSubmission(t *testing.T) {
	m, _, _ := managedFixture(t, time.Now().Add(24*time.Hour))
	m.httpClient.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})}
	_, release, err := m.BeginSubmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.RenewIfDue(context.Background()) }()
	select {
	case <-done:
		t.Fatal("credential renewal ran during a print submission")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("test renewal should fail on fixture HTTP 503")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renewal did not resume after submission")
	}
}
