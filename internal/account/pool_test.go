package account

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"grok-gateway/internal/config"
)

func TestPinnedSessionMovesWhenAccountCools(t *testing.T) {
	store := testStore(t, []config.Account{
		{Name: "one", AccessToken: "one", ExpiresAt: time.Now().Add(time.Hour), Enabled: true},
		{Name: "two", AccessToken: "two", ExpiresAt: time.Now().Add(time.Hour), Enabled: true},
	})
	pool := NewPool(store, OAuthClient{HTTP: http.DefaultClient})
	selected, _, err := pool.Select("session-1")
	if err != nil {
		t.Fatal(err)
	}
	selected.Release()
	selected.MarkCooldown(time.Minute)
	if other, pinned, err := pool.Select("session-1"); err != nil || pinned || other == selected {
		t.Fatalf("cooled session did not drift: account=%v pinned=%v err=%v", other, pinned, err)
	} else {
		other.Release()
	}
}

func testStore(t *testing.T, accounts []config.Account) *config.Store {
	t.Helper()
	cfg := config.Default()
	cfg.Accounts = accounts
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
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
