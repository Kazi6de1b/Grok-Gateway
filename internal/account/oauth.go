package account

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"grok-gateway/internal/config"
)

const (
	OAuthClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	OAuthScope    = "openid profile email offline_access grok-cli:access api:access"
	DeviceURL     = "https://auth.x.ai/oauth2/device/code"
	TokenURL      = "https://auth.x.ai/oauth2/token"
)

var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
	ErrAuthorizationDenied  = errors.New("authorization_denied")
)

type DeviceAuthorization struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                time.Duration
	ExpiresIn               time.Duration
}

type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time
}

type OAuthClient struct {
	HTTP           *http.Client
	DeviceEndpoint string
	TokenEndpoint  string
}

func (c OAuthClient) deviceEndpoint() string {
	if c.DeviceEndpoint != "" {
		return c.DeviceEndpoint
	}
	return DeviceURL
}

func (c OAuthClient) tokenEndpoint() string {
	if c.TokenEndpoint != "" {
		return c.TokenEndpoint
	}
	return TokenURL
}

func (c OAuthClient) StartDevice(ctx context.Context) (DeviceAuthorization, error) {
	form := url.Values{"client_id": {OAuthClientID}, "scope": {OAuthScope}}
	var payload struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
	}
	if err := c.postForm(ctx, c.deviceEndpoint(), form, &payload); err != nil {
		return DeviceAuthorization{}, err
	}
	if payload.DeviceCode == "" || payload.UserCode == "" || payload.VerificationURI == "" {
		return DeviceAuthorization{}, errors.New("Device OAuth 返回字段不完整")
	}
	if payload.Interval <= 0 {
		payload.Interval = 5
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 1800
	}
	return DeviceAuthorization{
		DeviceCode: payload.DeviceCode, UserCode: payload.UserCode,
		VerificationURI: payload.VerificationURI, VerificationURIComplete: payload.VerificationURIComplete,
		Interval: time.Duration(payload.Interval) * time.Second, ExpiresIn: time.Duration(payload.ExpiresIn) * time.Second,
	}, nil
}

func (c OAuthClient) PollDevice(ctx context.Context, deviceCode string) (Tokens, error) {
	return c.exchange(ctx, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":  {OAuthClientID}, "device_code": {deviceCode},
	}, "")
}

func (c OAuthClient) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	return c.exchange(ctx, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {OAuthClientID}, "refresh_token": {refreshToken},
	}, refreshToken)
}

func (c OAuthClient) exchange(ctx context.Context, form url.Values, fallbackRefresh string) (Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Tokens{}, err
	}
	var payload struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		IDToken          string `json:"id_token"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Tokens{}, fmt.Errorf("解析 OAuth 响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch payload.Error {
		case "authorization_pending":
			return Tokens{}, ErrAuthorizationPending
		case "slow_down":
			return Tokens{}, ErrSlowDown
		case "access_denied", "expired_token":
			return Tokens{}, ErrAuthorizationDenied
		default:
			return Tokens{}, fmt.Errorf("OAuth 返回 %d (%s): %s", resp.StatusCode, payload.Error, payload.ErrorDescription)
		}
	}
	if payload.AccessToken == "" {
		return Tokens{}, errors.New("OAuth 响应缺少 access_token")
	}
	if payload.ExpiresIn <= 0 {
		payload.ExpiresIn = 3600
	}
	if payload.RefreshToken == "" {
		payload.RefreshToken = fallbackRefresh
	}
	return Tokens{
		AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, IDToken: payload.IDToken,
		ExpiresAt: time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func (c OAuthClient) postForm(ctx context.Context, endpoint string, form url.Values, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OAuth 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, output)
}

func AccountFromTokens(name string, tokens Tokens) config.Account {
	claims := decodeClaims(firstNonEmpty(tokens.IDToken, tokens.AccessToken))
	userID := stringClaim(claims, "sub")
	email := stringClaim(claims, "email")
	if strings.TrimSpace(name) == "" {
		name = firstNonEmpty(email, userID, "Grok Build account")
	}
	return config.Account{
		Name: name, Kind: config.AccountKindOAuth,
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		ExpiresAt: tokens.ExpiresAt, UserID: userID, Email: email, Enabled: true,
	}
}

func decodeClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

func stringClaim(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ParseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}
