package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tokenManager(t *testing.T, handler http.HandlerFunc) (*TokenManager, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewTokenManager(AuthConfig{
		ClientID: "test-client", Scope: "test-scope",
		TokenEndpoint: srv.URL, DeviceEndpoint: srv.URL,
		TokenPath: filepath.Join(t.TempDir(), "token.json"),
	}), srv
}

func TestAccessTokenLifecycle(t *testing.T) {
	var requests atomic.Int32
	m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-ClientType") != "SmartClientForMac" {
			t.Error("missing native token-exchange client header")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "test-refresh" {
			t.Errorf("unexpected form: %v", r.Form)
		}
		w.Write([]byte(`{"access_token":"test-new","token_type":"Bearer","expires_in":3600}`))
	})
	m.token = &TokenResponse{
		AccessToken: "test-old", RefreshToken: "test-refresh", TokenType: "Bearer",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if token, err := m.AccessToken(context.Background()); err != nil || token != "test-old" || requests.Load() != 0 {
		t.Fatalf("fresh token: %q, %v, requests=%d", token, err, requests.Load())
	}
	m.token.ExpiresAt = time.Now().Add(-time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := m.AccessToken(context.Background())
			if err != nil || token != "test-new" {
				t.Errorf("refreshed token: %q, %v", token, err)
			}
		}()
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("refresh requests = %d, want 1", requests.Load())
	}
	restored := NewTokenManager(m.cfg)
	if err := restored.Load(); err != nil {
		t.Fatal(err)
	}
	if restored.token.RefreshToken != "test-refresh" || restored.token.ExpiresAt.IsZero() {
		t.Fatalf("lost persisted state: %+v", restored.token)
	}
	info, err := os.Stat(m.cfg.TokenPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("token permissions: %v, %v", info, err)
	}
}

func TestTokenFailures(t *testing.T) {
	for _, response := range []string{
		`{"access_token":"","token_type":"Bearer","expires_in":3600}`,
		`{"access_token":"test","token_type":"Basic","expires_in":3600}`,
		`{"access_token":"test","token_type":"Bearer","expires_in":0}`,
		`invalid JSON`,
	} {
		t.Run(response, func(t *testing.T) {
			m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(response)) })
			m.token = &TokenResponse{AccessToken: "old", TokenType: "Bearer", RefreshToken: "test-refresh"}
			if _, err := m.AccessToken(context.Background()); err == nil {
				t.Fatal("invalid response accepted")
			}

			if m.token.AccessToken != "old" {
				t.Fatal("invalid response replaced token")
			}
		})
	}
	m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"test-private-value"}`))
	})
	m.token = &TokenResponse{AccessToken: "old", TokenType: "Bearer", RefreshToken: "test-refresh"}
	if _, err := m.AccessToken(context.Background()); err == nil || err.Error() != "token refresh: OAuth error: invalid_grant" {
		t.Fatalf("unexpected sanitized error: %v", err)
	}
	m.token.RefreshToken = ""
	if _, err := m.AccessToken(context.Background()); err == nil {
		t.Fatal("unknown expiration treated as valid")
	}
}

func TestNativeRefreshWithImplicitBearer(t *testing.T) {
	for _, typeField := range []string{"", `,"token_type":""`, `,"token_type":null`} {
		t.Run(typeField, func(t *testing.T) {
			m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"access_token":"test-native","expires_in":3600` + typeField + `}`))
			})
			m.cfg.ClientSecret = "test-client-secret"
			m.token = &TokenResponse{AccessToken: "old", TokenType: "Bearer", RefreshToken: "test-refresh"}
			got, err := m.AccessToken(context.Background())
			if err != nil || got != "test-native" {
				t.Fatalf("native refresh failed: %v", err)
			}
			restored := NewTokenManager(m.cfg)
			if err := restored.Load(); err != nil {
				t.Fatal(err)
			}
			if restored.token.TokenType != "Bearer" {
				t.Fatal("implicit native token type was not normalized for persistence")
			}
		})
	}
}

func TestNativeRefreshRejectsExplicitUnsupportedType(t *testing.T) {
	for _, typeField := range []string{`"Basic"`, `42`, `true`} {
		t.Run(typeField, func(t *testing.T) {
			m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`{"access_token":"test-native","expires_in":3600,"token_type":` + typeField + `}`))
			})
			m.cfg.ClientSecret = "test-client-secret"
			m.token = &TokenResponse{AccessToken: "old", TokenType: "Bearer", RefreshToken: "test-refresh"}
			if _, err := m.AccessToken(context.Background()); err == nil {
				t.Fatal("unsupported explicit type was accepted")
			}
			if m.token.AccessToken != "old" {
				t.Fatal("failed refresh replaced the old token")
			}
		})
	}
}

func TestSaveReplacesInsecureFile(t *testing.T) {
	m, _ := tokenManager(t, func(http.ResponseWriter, *http.Request) {})
	if err := os.WriteFile(m.cfg.TokenPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	m.token = &TokenResponse{AccessToken: "test", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(m.cfg.TokenPath)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v, %v", stat, err)
	}
}

func TestDevicePolling(t *testing.T) {
	var calls atomic.Int32
	m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"authorization_pending"}`))
		default:
			w.Write([]byte(`{"access_token":"test-device","token_type":"Bearer","expires_in":3600}`))
		}
	})
	token, err := m.PollForToken(context.Background(), &DeviceAuthorizationResponse{
		DeviceCode: "test-code", ExpiresIn: 10, Interval: 1,
	})
	if err != nil || token.AccessToken != "test-device" || calls.Load() != 2 {
		t.Fatalf("poll result: %v, %v, calls=%d", token, err, calls.Load())
	}
}

func TestDeviceSlowDownAndDenial(t *testing.T) {
	var calls atomic.Int32
	m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		code := "slow_down"
		if calls.Add(1) > 1 {
			code = "access_denied"
		}
		json.NewEncoder(w).Encode(OAuthError{Code: code})
	})
	start := time.Now()
	_, err := m.PollForToken(context.Background(), &DeviceAuthorizationResponse{
		DeviceCode: "test-code", ExpiresIn: 15, Interval: 1,
	})
	var oauthErr *OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Code != "access_denied" || time.Since(start) < 7*time.Second || calls.Load() != 2 {
		t.Fatalf("slow_down/denial: %v, duration=%v, calls=%d", err, time.Since(start), calls.Load())
	}
}

func TestDeviceCancellationAndInvalidResponse(t *testing.T) {
	m, _ := tokenManager(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	if _, err := m.BeginDeviceAuthorization(context.Background()); err == nil {
		t.Fatal("empty device response accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.PollForToken(ctx, &DeviceAuthorizationResponse{DeviceCode: "test", ExpiresIn: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := m.PollForToken(context.Background(), nil); err == nil {
		t.Fatal("nil device response accepted")
	}
}
