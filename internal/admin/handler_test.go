package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"grok-gateway/internal/account"
	"grok-gateway/internal/config"
)

func TestDashboardAndStateDoNotExposeTokens(t *testing.T) {
	store := adminTestStore(t)
	oauth := account.OAuthClient{HTTP: http.DefaultClient}
	pool := account.NewPool(store, oauth)
	handler, err := NewHandler(http.NotFoundHandler(), store, pool, http.DefaultClient, oauth, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Grok Gateway") {
		t.Fatalf("dashboard response code=%d", page.Code)
	}
	state := httptest.NewRecorder()
	handler.ServeHTTP(state, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if state.Code != http.StatusOK || strings.Contains(state.Body.String(), "secret-access") || strings.Contains(state.Body.String(), "secret-refresh") {
		t.Fatalf("state leaked credentials: %s", state.Body.String())
	}
}

func TestSettingsAPIStoresConfiguration(t *testing.T) {
	store := adminTestStore(t)
	oauth := account.OAuthClient{HTTP: http.DefaultClient}
	pool := account.NewPool(store, oauth)
	handler, _ := NewHandler(http.NotFoundHandler(), store, pool, http.DefaultClient, oauth, "test", nil)
	body := `{"listen":"127.0.0.1:9900","upstream_base_url":"https://cli-chat-proxy.grok.com/v1","outbound_proxy":"http://127.0.0.1:7891","cooldown":"10m"}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		data, _ := io.ReadAll(recorder.Result().Body)
		t.Fatalf("settings response=%d %s", recorder.Code, data)
	}
	cfg := store.Snapshot()
	if cfg.Listen != "127.0.0.1:9900" || cfg.OutboundProxy != "http://127.0.0.1:7891" || cfg.Cooldown != "10m" {
		t.Fatalf("settings not saved: %#v", cfg)
	}
}

func adminTestStore(t *testing.T) *config.Store {
	t.Helper()
	cfg := config.Default()
	cfg.Accounts = []config.Account{{
		Name: "test", AccessToken: "secret-access", RefreshToken: "secret-refresh",
		ExpiresAt: time.Now().Add(time.Hour), Enabled: true,
	}}
	data, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, _, err := config.LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
