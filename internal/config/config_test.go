package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadCreateAndUpdateAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, created, err := LoadOrCreate(path)
	if err != nil || !created {
		t.Fatalf("LoadOrCreate() created=%v err=%v", created, err)
	}
	value := Account{
		Name: "test", AccessToken: "old", RefreshToken: "refresh", UserID: "user-1",
		ExpiresAt: time.Now().Add(time.Hour).UTC(), Enabled: true,
	}
	if err := store.UpsertAccount(value); err != nil {
		t.Fatal(err)
	}
	newExpiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := store.UpdateTokens(AccountIdentity(value), "new", "refresh-2", newExpiry); err != nil {
		t.Fatal(err)
	}
	reloaded, created, err := LoadOrCreate(path)
	if err != nil || created {
		t.Fatalf("reload created=%v err=%v", created, err)
	}
	accounts := reloaded.Snapshot().Accounts
	if len(accounts) != 1 || accounts[0].AccessToken != "new" || accounts[0].RefreshToken != "refresh-2" || !accounts[0].ExpiresAt.Equal(newExpiry) {
		t.Fatalf("unexpected account: %#v", accounts)
	}
}

func TestConfigRejectsPublicListener(t *testing.T) {
	value := Default()
	value.Listen = "0.0.0.0:8787"
	if value.Validate() == nil {
		t.Fatal("public listener should be rejected")
	}
}
