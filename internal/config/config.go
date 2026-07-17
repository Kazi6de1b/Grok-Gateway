package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultListen          = "127.0.0.1:8787"
	DefaultUpstreamBaseURL = "https://cli-chat-proxy.grok.com/v1"
	DefaultOutboundProxy   = "http://127.0.0.1:7890"
)

const (
	AccountKindOAuth  = "oauth"
	AccountKindAPIKey = "api_key"
)

type Account struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind,omitempty"` // oauth | api_key
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	UserID       string    `json:"user_id,omitempty"`
	Email        string    `json:"email,omitempty"`
	Enabled      bool      `json:"enabled"`
	Usage        *Usage    `json:"usage,omitempty"`
}

type Usage struct {
	PlanName           string    `json:"plan_name,omitempty"`
	MonthlyLimit       float64   `json:"monthly_limit,omitempty"`
	Used               float64   `json:"used,omitempty"`
	OnDemandCap        float64   `json:"on_demand_cap,omitempty"`
	OnDemandUsed       float64   `json:"on_demand_used,omitempty"`
	PrepaidBalance     float64   `json:"prepaid_balance,omitempty"`
	UsagePercent       float64   `json:"usage_percent"`
	UsagePeriodStart   string    `json:"usage_period_start,omitempty"`
	UsagePeriodEnd     string    `json:"usage_period_end,omitempty"`
	BillingPeriodStart string    `json:"billing_period_start,omitempty"`
	BillingPeriodEnd   string    `json:"billing_period_end,omitempty"`
	SyncedAt           time.Time `json:"synced_at"`
}

type Config struct {
	Listen           string    `json:"listen"`
	UpstreamBaseURL  string    `json:"upstream_base_url"`
	OutboundProxy    string    `json:"outbound_proxy"`
	Cooldown         string    `json:"cooldown"`
	PreferredAccount string    `json:"preferred_account,omitempty"`
	// GatewayAPIKey protects /v1/* when RequireAPIKey is true.
	GatewayAPIKey string `json:"gateway_api_key,omitempty"`
	RequireAPIKey bool   `json:"require_api_key,omitempty"`
	// LogRequestBodies stores truncated request body previews in request logs (privacy risk).
	LogRequestBodies bool      `json:"log_request_bodies,omitempty"`
	Accounts         []Account `json:"accounts"`
}

func Default() Config {
	return Config{
		Listen:          DefaultListen,
		UpstreamBaseURL: DefaultUpstreamBaseURL,
		OutboundProxy:   DefaultOutboundProxy,
		Cooldown:        "5m",
		Accounts:        []Account{},
	}
}

func (c Config) Validate() error {
	if !strings.HasPrefix(strings.TrimSpace(c.Listen), "127.0.0.1:") && !strings.HasPrefix(strings.TrimSpace(c.Listen), "localhost:") {
		return errors.New("listen 必须绑定到 127.0.0.1 或 localhost")
	}
	for name, raw := range map[string]string{"upstream_base_url": c.UpstreamBaseURL, "outbound_proxy": c.OutboundProxy} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("%s 不是有效 URL", name)
		}
	}
	if _, err := time.ParseDuration(c.Cooldown); err != nil {
		return fmt.Errorf("cooldown 不是有效时长: %w", err)
	}
	return nil
}

func (c Config) CooldownDuration() time.Duration {
	value, err := time.ParseDuration(c.Cooldown)
	if err != nil {
		return 5 * time.Minute
	}
	return value
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func LoadOrCreate(path string) (*Store, bool, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		store := &Store{path: path, cfg: Default()}
		if err := store.saveLocked(); err != nil {
			return nil, false, err
		}
		return store, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, false, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, false, fmt.Errorf("配置无效: %w", err)
	}
	return &Store{path: path, cfg: cfg}, false, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value := s.cfg
	value.Accounts = append([]Account(nil), s.cfg.Accounts...)
	return value
}

func (s *Store) UpsertAccount(value Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Accounts {
		if accountIdentity(s.cfg.Accounts[index]) == accountIdentity(value) {
			s.cfg.Accounts[index] = value
			return s.saveLocked()
		}
	}
	s.cfg.Accounts = append(s.cfg.Accounts, value)
	return s.saveLocked()
}

func (s *Store) UpdateTokens(identity, accessToken, refreshToken string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Accounts {
		if accountIdentity(s.cfg.Accounts[index]) != identity {
			continue
		}
		s.cfg.Accounts[index].AccessToken = accessToken
		if refreshToken != "" {
			s.cfg.Accounts[index].RefreshToken = refreshToken
		}
		s.cfg.Accounts[index].ExpiresAt = expiresAt
		return s.saveLocked()
	}
	return fmt.Errorf("账号 %q 不存在", identity)
}

func (s *Store) UpdateUsage(identity string, usage Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Accounts {
		if accountIdentity(s.cfg.Accounts[index]) == identity {
			s.cfg.Accounts[index].Usage = &usage
			return s.saveLocked()
		}
	}
	return fmt.Errorf("账号 %q 不存在", identity)
}

func (s *Store) SetPreferredAccount(identity string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if identity != "" {
		found := false
		for _, value := range s.cfg.Accounts {
			if accountIdentity(value) == identity {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("账号 %q 不存在", identity)
		}
	}
	s.cfg.PreferredAccount = identity
	return s.saveLocked()
}

func (s *Store) SetAccountEnabled(identity string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Accounts {
		if accountIdentity(s.cfg.Accounts[index]) == identity {
			s.cfg.Accounts[index].Enabled = enabled
			return s.saveLocked()
		}
	}
	return fmt.Errorf("账号 %q 不存在", identity)
}

func (s *Store) DeleteAccount(identity string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.cfg.Accounts {
		if accountIdentity(s.cfg.Accounts[index]) != identity {
			continue
		}
		s.cfg.Accounts = append(s.cfg.Accounts[:index], s.cfg.Accounts[index+1:]...)
		if s.cfg.PreferredAccount == identity {
			s.cfg.PreferredAccount = ""
		}
		return s.saveLocked()
	}
	return fmt.Errorf("账号 %q 不存在", identity)
}

func (s *Store) UpdateSettings(listen, upstream, outboundProxy, cooldown string, requireAPIKey *bool, gatewayAPIKey *string, logBodies *bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	next.Listen = strings.TrimSpace(listen)
	next.UpstreamBaseURL = strings.TrimSpace(upstream)
	next.OutboundProxy = strings.TrimSpace(outboundProxy)
	next.Cooldown = strings.TrimSpace(cooldown)
	if requireAPIKey != nil {
		next.RequireAPIKey = *requireAPIKey
	}
	if gatewayAPIKey != nil {
		next.GatewayAPIKey = strings.TrimSpace(*gatewayAPIKey)
	}
	if logBodies != nil {
		next.LogRequestBodies = *logBodies
	}
	if err := next.Validate(); err != nil {
		return err
	}
	s.cfg = next
	return s.saveLocked()
}

func (s *Store) SetGatewayAPIKey(key string, require bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.GatewayAPIKey = strings.TrimSpace(key)
	s.cfg.RequireAPIKey = require
	return s.saveLocked()
}

func (s *Store) AddAPIKeyAccount(name, apiKey string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return Account{}, errors.New("API Key 不能为空")
	}
	if name == "" {
		name = "api-key-" + shortID(apiKey)
	}
	value := Account{
		Name: name, Kind: AccountKindAPIKey, AccessToken: apiKey, Enabled: true,
	}
	for index := range s.cfg.Accounts {
		if accountIdentity(s.cfg.Accounts[index]) == accountIdentity(value) || s.cfg.Accounts[index].Name == name {
			s.cfg.Accounts[index] = value
			return value, s.saveLocked()
		}
	}
	s.cfg.Accounts = append(s.cfg.Accounts, value)
	return value, s.saveLocked()
}

func shortID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[len(value)-8:]
}

func accountIdentity(value Account) string {
	if value.UserID != "" {
		return "user:" + value.UserID
	}
	if value.Email != "" {
		return "email:" + strings.ToLower(value.Email)
	}
	if value.Kind == AccountKindAPIKey {
		// Stable id for explicit API key accounts without OAuth subject.
		sum := value.Name
		if sum == "" {
			sum = shortID(value.AccessToken)
		}
		return "apikey:" + strings.ToLower(sum)
	}
	return "name:" + value.Name
}

func AccountIdentity(value Account) string { return accountIdentity(value) }

func (s *Store) saveLocked() error {
	if err := s.cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, s.path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
