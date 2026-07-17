package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"grok-gateway/internal/account"
	"grok-gateway/internal/config"
)

func TestProxyPreservesProtocolAndPinsSession(t *testing.T) {
	var mu sync.Mutex
	var tokens []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens = append(tokens, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path != "/v1/responses" || r.URL.RawQuery != "trace=1" {
			t.Errorf("unexpected upstream URL: %s", r.URL.String())
		}
		if r.Header.Get("x-grok-user-id") == "" || r.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" {
			t.Errorf("missing build headers: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.UpstreamBaseURL = upstream.URL + "/v1"
	cfg.Accounts = []config.Account{
		{Name: "one", AccessToken: "token-one", UserID: "u1", ExpiresAt: time.Now().Add(time.Hour), Enabled: true},
		{Name: "two", AccessToken: "token-two", UserID: "u2", ExpiresAt: time.Now().Add(time.Hour), Enabled: true},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, _, err := config.LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	client := upstream.Client()
	pool := account.NewPool(store, account.OAuthClient{HTTP: client})
	handler, err := NewHandler(cfg, pool, client, nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses?trace=1", strings.NewReader(`{"input":"hello","model":"grok-4.5"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("x-grok-session-id", "same-session")
		request.Header.Set("Authorization", "Bearer downstream-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || recorder.Body.String() != "data: ok\n\n" {
			t.Fatalf("response code=%d body=%q", recorder.Code, recorder.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tokens) != 2 || tokens[0] == "Bearer downstream-token" || tokens[0] != tokens[1] {
		t.Fatalf("session was not pinned or auth not replaced: %#v", tokens)
	}
}

func TestSessionKeyFallsBackToPromptCacheKey(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(""))
	request.Header.Set("Content-Type", "application/json")
	if got := sessionKey(request, []byte(`{"prompt_cache_key":"cache-1"}`)); got != "prompt:cache-1" {
		t.Fatalf("sessionKey() = %q", got)
	}
}

func TestProxyFailsOverAfterRateLimit(t *testing.T) {
	var tokens []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		tokens = append(tokens, token)
		if token == "Bearer token-one" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	cfg := config.Default()
	cfg.UpstreamBaseURL = upstream.URL + "/v1"
	cfg.PreferredAccount = "name:one"
	cfg.Accounts = []config.Account{
		{Name: "one", AccessToken: "token-one", ExpiresAt: time.Now().Add(time.Hour), Enabled: true},
		{Name: "two", AccessToken: "token-two", ExpiresAt: time.Now().Add(time.Hour), Enabled: true},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, _, err := config.LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	pool := account.NewPool(store, account.OAuthClient{HTTP: upstream.Client()})
	handler, err := NewHandler(cfg, pool, upstream.Client(), nil, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"hello"}`))
	request.Header.Set("x-grok-session-id", "failover-session")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("response code=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if len(tokens) != 2 || tokens[0] != "Bearer token-one" || tokens[1] != "Bearer token-two" {
		t.Fatalf("unexpected failover sequence: %#v", tokens)
	}
}
