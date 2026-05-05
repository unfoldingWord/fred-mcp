package main

import (
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
	oauthClientID string
	allowedHD     string
	toolboxURL    string
	tokeninfoURL  string
)

func main() {
	oauthClientID = requireEnv("OAUTH_CLIENT_ID")
	toolboxURL = requireEnv("TOOLBOX_URL")
	allowedHD = getEnv("ALLOWED_HD", "unfoldingword.org")
	tokeninfoURL = getEnv("TOKENINFO_URL", "https://oauth2.googleapis.com/tokeninfo")
	listenAddr := getEnv("LISTEN_ADDR", "127.0.0.1:5500")

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

	// Cache miss or expired — call Google tokeninfo.
	resp, err := http.PostForm(tokeninfoURL, url.Values{"access_token": {token}})
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
	if info.Aud != oauthClientID {
		log.Printf("tokeninfo: aud mismatch: got %q, want %q", info.Aud, oauthClientID)
		unauthorized(w)
		return
	}
	if info.HD != allowedHD {
		log.Printf("tokeninfo: hd mismatch: got %q, want %q", info.HD, allowedHD)
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
		"resource":                toolboxURL,
		"authorization_servers":   []string{"https://accounts.google.com"},
		"scopes_supported":        []string{"openid", "email", "profile"},
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
