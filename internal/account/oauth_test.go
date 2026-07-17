package account

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccountFromTokensUsesJWTClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-1","email":"me@example.com"}`))
	token := "header." + payload + ".signature"
	expires := time.Now().Add(time.Hour).UTC()
	value := AccountFromTokens("", Tokens{AccessToken: token, RefreshToken: "refresh", ExpiresAt: expires})
	if value.Name != "me@example.com" || value.UserID != "user-1" || value.Email != "me@example.com" || !value.Enabled {
		t.Fatalf("unexpected account: %#v", value)
	}
}

func TestRefreshParsesOAuthTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "old-refresh" {
			t.Errorf("unexpected refresh request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer server.Close()
	client := OAuthClient{HTTP: server.Client(), TokenEndpoint: server.URL}
	tokens, err := client.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "new-access" || tokens.RefreshToken != "new-refresh" || time.Until(tokens.ExpiresAt) < 59*time.Minute {
		t.Fatalf("unexpected tokens: %#v", tokens)
	}
}

func TestRefreshKeepsFallbackRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600}`))
	}))
	defer server.Close()
	tokens, err := (OAuthClient{HTTP: server.Client(), TokenEndpoint: server.URL}).Refresh(context.Background(), "keep-me")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(tokens.RefreshToken, "keep-me") {
		t.Fatalf("refresh token = %q", tokens.RefreshToken)
	}
}
