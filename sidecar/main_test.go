package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// setupSidecar configures global state and returns a test tokeninfo server.
// The handler function is called for each tokeninfo request.
func setupSidecar(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Configure sidecar globals.
	oauthClientIDs = map[string]bool{"test-client-id.apps.googleusercontent.com": true}
	allowedHD = "unfoldingword.org"
	toolboxURL = "https://fred-mcp.fly.dev"
	tokeninfoURL = ts.URL

	// Clear cache between tests.
	cacheMu.Lock()
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()

	return ts
}

func validTokeninfoResponse() []byte {
	resp, _ := json.Marshal(map[string]string{
		"aud":            "test-client-id.apps.googleusercontent.com",
		"sub":            "1234567890",
		"email":          "user@unfoldingword.org",
		"email_verified": "true",
		"hd":             "unfoldingword.org",
		"expires_in":     "3600",
	})
	return resp
}

func TestVerify_MissingAuthHeader(t *testing.T) {
	setupSidecar(t, nil)

	req := httptest.NewRequest("GET", "/verify", nil)
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("expected WWW-Authenticate header")
	}
}

func TestVerify_MalformedBearer(t *testing.T) {
	setupSidecar(t, nil)

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestVerify_EmptyBearer(t *testing.T) {
	setupSidecar(t, nil)

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestVerify_WrongAud(t *testing.T) {
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		resp, _ := json.Marshal(map[string]string{
			"aud":            "wrong-client-id.apps.googleusercontent.com",
			"sub":            "1234567890",
			"email":          "user@unfoldingword.org",
			"email_verified": "true",
			"hd":             "unfoldingword.org",
			"expires_in":     "3600",
		})
		w.Write(resp)
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.valid-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestVerify_AudSkippedWhenNoAllowList(t *testing.T) {
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		// Return a token with a different aud — should pass when allow-list is empty.
		resp, _ := json.Marshal(map[string]string{
			"aud":            "some-other-client.apps.googleusercontent.com",
			"sub":            "1234567890",
			"email":          "user@unfoldingword.org",
			"email_verified": "true",
			"hd":             "unfoldingword.org",
			"expires_in":     "3600",
		})
		w.Write(resp)
	})

	// Clear the allow-list — audience validation should be skipped.
	oauthClientIDs = map[string]bool{}

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.any-client-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (aud skipped), got %d", w.Code)
	}
}

func TestVerify_WrongEmailDomain(t *testing.T) {
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		resp, _ := json.Marshal(map[string]string{
			"aud":            "test-client-id.apps.googleusercontent.com",
			"sub":            "1234567890",
			"email":          "user@gmail.com",
			"email_verified": "true",
			"hd":             "gmail.com",
			"expires_in":     "3600",
		})
		w.Write(resp)
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.valid-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestVerify_EmailNotVerified(t *testing.T) {
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		resp, _ := json.Marshal(map[string]string{
			"aud":            "test-client-id.apps.googleusercontent.com",
			"sub":            "1234567890",
			"email":          "user@unfoldingword.org",
			"email_verified": "false",
			"hd":             "unfoldingword.org",
			"expires_in":     "3600",
		})
		w.Write(resp)
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.valid-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestVerify_ExpiredToken(t *testing.T) {
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		resp, _ := json.Marshal(map[string]string{
			"aud":            "test-client-id.apps.googleusercontent.com",
			"sub":            "1234567890",
			"email":          "user@unfoldingword.org",
			"email_verified": "true",
			"hd":             "unfoldingword.org",
			"expires_in":     "0",
		})
		w.Write(resp)
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.valid-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestVerify_GoogleReturnsNon200(t *testing.T) {
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": "invalid_token"}`)
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.invalid-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestVerify_ValidToken(t *testing.T) {
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(validTokeninfoResponse())
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.valid-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Auth-Email"); got != "user@unfoldingword.org" {
		t.Fatalf("expected X-Auth-Email=user@unfoldingword.org, got %q", got)
	}
	if got := w.Header().Get("X-Auth-Sub"); got != "1234567890" {
		t.Fatalf("expected X-Auth-Sub=1234567890, got %q", got)
	}
	if got := w.Header().Get("X-Auth-Hd"); got != "unfoldingword.org" {
		t.Fatalf("expected X-Auth-Hd=unfoldingword.org, got %q", got)
	}
}

func TestVerify_ValidToken_NoHdClaim(t *testing.T) {
	// Google may omit `hd` from access token tokeninfo responses.
	// The domain gate uses email suffix, so this should still pass.
	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		resp, _ := json.Marshal(map[string]string{
			"aud":            "test-client-id.apps.googleusercontent.com",
			"sub":            "1234567890",
			"email":          "user@unfoldingword.org",
			"email_verified": "true",
			"expires_in":     "3600",
			// Note: no "hd" field
		})
		w.Write(resp)
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.no-hd-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (hd not required), got %d", w.Code)
	}
}

func TestVerify_CacheHit(t *testing.T) {
	var callCount atomic.Int32

	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Write(validTokeninfoResponse())
	})

	// First request — cache miss, hits tokeninfo.
	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.cache-test-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w.Code)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 tokeninfo call, got %d", callCount.Load())
	}

	// Second request — cache hit, should NOT hit tokeninfo.
	req2 := httptest.NewRequest("GET", "/verify", nil)
	req2.Header.Set("Authorization", "Bearer ya29.cache-test-token")
	w2 := httptest.NewRecorder()
	handleVerify(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("second request: expected 200, got %d", w2.Code)
	}
	if callCount.Load() != 1 {
		t.Fatalf("expected tokeninfo NOT called again, got %d calls", callCount.Load())
	}
}

func TestVerify_CacheExpiry(t *testing.T) {
	var callCount atomic.Int32

	setupSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		// Return a token that expires in 1 second.
		resp, _ := json.Marshal(map[string]string{
			"aud":            "test-client-id.apps.googleusercontent.com",
			"sub":            "1234567890",
			"email":          "user@unfoldingword.org",
			"email_verified": "true",
			"hd":             "unfoldingword.org",
			"expires_in":     "1",
		})
		w.Write(resp)
	})

	req := httptest.NewRequest("GET", "/verify", nil)
	req.Header.Set("Authorization", "Bearer ya29.expiry-test-token")
	w := httptest.NewRecorder()
	handleVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w.Code)
	}

	// Wait for cache to expire.
	time.Sleep(1100 * time.Millisecond)

	// Second request — cache expired, should hit tokeninfo again.
	req2 := httptest.NewRequest("GET", "/verify", nil)
	req2.Header.Set("Authorization", "Bearer ya29.expiry-test-token")
	w2 := httptest.NewRecorder()
	handleVerify(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("second request: expected 200, got %d", w2.Code)
	}
	if callCount.Load() != 2 {
		t.Fatalf("expected 2 tokeninfo calls after expiry, got %d", callCount.Load())
	}
}

func TestPRM_ReturnsValidJSON(t *testing.T) {
	setupSidecar(t, nil)

	req := httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()
	handlePRM(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var prm map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &prm); err != nil {
		t.Fatalf("failed to parse PRM JSON: %v", err)
	}

	if prm["resource"] != "https://fred-mcp.fly.dev" {
		t.Fatalf("expected resource=https://fred-mcp.fly.dev, got %v", prm["resource"])
	}

	servers, ok := prm["authorization_servers"].([]interface{})
	if !ok || len(servers) == 0 || servers[0] != "https://accounts.google.com" {
		t.Fatalf("unexpected authorization_servers: %v", prm["authorization_servers"])
	}
}

func TestEmailDomainOf(t *testing.T) {
	tests := []struct {
		email string
		want  string
	}{
		{"user@unfoldingword.org", "unfoldingword.org"},
		{"user@Gmail.Com", "gmail.com"},
		{"nodomain", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := emailDomainOf(tt.email)
		if got != tt.want {
			t.Errorf("emailDomainOf(%q) = %q, want %q", tt.email, got, tt.want)
		}
	}
}
