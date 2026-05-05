package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	email     string
	sub       string
	hd        string
	expiresAt time.Time
}

var (
	cache   = make(map[string]cacheEntry)
	cacheMu sync.RWMutex
)

var (
	// oauthClientIDs is the set of acceptable audience values.
	// If empty, audience validation is skipped (rely on domain gate +
	// Internal consent screen). Comma-separated in the env var.
	oauthClientIDs map[string]bool
	allowedHD      string
	toolboxURL     string
	tokeninfoURL   string
)

// httpClient is used for tokeninfo requests with a bounded timeout.
var httpClient = &http.Client{Timeout: 5 * time.Second}

func main() {
	toolboxURL = requireEnv("TOOLBOX_URL")
	allowedHD = getEnv("ALLOWED_HD", "unfoldingword.org")
	tokeninfoURL = getEnv("TOKENINFO_URL", "https://oauth2.googleapis.com/tokeninfo")
	listenAddr := getEnv("LISTEN_ADDR", "127.0.0.1:5500")

	// OAUTH_CLIENT_IDS: comma-separated list of accepted audiences.
	// Required — the sidecar refuses to start without at least one.
	// MCP clients must use one of these client_ids when authenticating
	// with Google, so the resulting token's aud matches our allow-list.
	oauthClientIDs = parseClientIDs(requireEnv("OAUTH_CLIENT_IDS"))
	if len(oauthClientIDs) == 0 {
		log.Fatalf("tokeninfo-sidecar: OAUTH_CLIENT_IDS is set but contains no valid IDs")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/verify", handleVerify)
	mux.HandleFunc("/.well-known/oauth-protected-resource", handlePRM)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	log.Printf("tokeninfo-sidecar listening on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("tokeninfo-sidecar: %v", err)
	}
}

func handleVerify(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		unauthorized(w)
		return
	}

	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || token == "" {
		unauthorized(w)
		return
	}

	// Check cache.
	cacheMu.RLock()
	entry, cached := cache[token]
	cacheMu.RUnlock()

	if cached && time.Now().Before(entry.expiresAt) {
		writeSuccess(w, entry)
		return
	}

	// Cache miss or expired — call Google tokeninfo with bounded timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	form := url.Values{"access_token": {token}}
	req, err := http.NewRequestWithContext(ctx, "POST", tokeninfoURL, strings.NewReader(form.Encode()))
	if err != nil {
		log.Printf("tokeninfo request build failed: %v", err)
		unauthorized(w)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("tokeninfo request failed: %v", err)
		unauthorized(w)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		unauthorized(w)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("tokeninfo read body failed: %v", err)
		unauthorized(w)
		return
	}

	var info struct {
		Aud           string `json:"aud"`
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified string `json:"email_verified"`
		HD            string `json:"hd"`
		ExpiresIn     string `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		log.Printf("tokeninfo parse failed: %v", err)
		unauthorized(w)
		return
	}

	// Validate claims.

	// Audience check: the token must have been issued for one of our
	// registered client_ids. This ensures tokens from unrelated Google
	// services cannot be replayed to this resource server (MCP spec §4).
	if !oauthClientIDs[info.Aud] {
		log.Printf("tokeninfo: aud %q not in allowed set", info.Aud)
		unauthorized(w)
		return
	}

	// Domain gate: check email suffix. This is always present in
	// tokeninfo when the token has the email scope, unlike the hd
	// claim which is not guaranteed for access token introspection.
	emailDomain := emailDomainOf(info.Email)
	if emailDomain != allowedHD {
		log.Printf("tokeninfo: email domain %q != allowed %q (email: %s)", emailDomain, allowedHD, info.Email)
		unauthorized(w)
		return
	}

	if info.EmailVerified != "true" {
		log.Printf("tokeninfo: email_verified is %q for %s", info.EmailVerified, info.Email)
		unauthorized(w)
		return
	}

	expiresIn, err := strconv.Atoi(info.ExpiresIn)
	if err != nil || expiresIn <= 0 {
		log.Printf("tokeninfo: token expired or invalid expires_in: %q", info.ExpiresIn)
		unauthorized(w)
		return
	}

	// Cache the result. TTL = min(expires_in, 60s).
	ttl := time.Duration(expiresIn) * time.Second
	if ttl > 60*time.Second {
		ttl = 60 * time.Second
	}

	entry = cacheEntry{
		email:     info.Email,
		sub:       info.Sub,
		hd:        info.HD,
		expiresAt: time.Now().Add(ttl),
	}

	cacheMu.Lock()
	cache[token] = entry
	cacheMu.Unlock()

	writeSuccess(w, entry)
}

func handlePRM(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource":                 toolboxURL,
		"authorization_servers":    []string{"https://accounts.google.com"},
		"scopes_supported":         []string{"openid", "email", "profile"},
		"bearer_methods_supported": []string{"header"},
	})
}

func writeSuccess(w http.ResponseWriter, entry cacheEntry) {
	w.Header().Set("X-Auth-Email", entry.email)
	w.Header().Set("X-Auth-Sub", entry.sub)
	w.Header().Set("X-Auth-Hd", entry.hd)
	w.WriteHeader(http.StatusOK)
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate",
		fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, toolboxURL))
	w.WriteHeader(http.StatusUnauthorized)
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("tokeninfo-sidecar: required env var %s is not set", key)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseClientIDs splits a comma-separated string into a set.
// Returns an empty map if the input is empty.
func parseClientIDs(raw string) map[string]bool {
	ids := make(map[string]bool)
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			ids[id] = true
		}
	}
	return ids
}

// emailDomainOf extracts the domain part of an email address.
func emailDomainOf(email string) string {
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(domain)
}
