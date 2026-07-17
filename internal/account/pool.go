package account

import (
	"context"
	"errors"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"grok-gateway/internal/config"
)

var ErrNoAvailableAccount = errors.New("当前没有可用的 Grok Build 账号")

type RuntimeAccount struct {
	mu            sync.Mutex
	value         config.Account
	cooldownUntil time.Time
	inFlight      atomic.Int64
	store         *config.Store
	oauth         OAuthClient
}

func (a *RuntimeAccount) Name() string {
	return a.Snapshot().Name
}

func (a *RuntimeAccount) Identity() string {
	return config.AccountIdentity(a.Snapshot())
}

func (a *RuntimeAccount) Snapshot() config.Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	value := a.value
	if value.Usage != nil {
		usage := *value.Usage
		value.Usage = &usage
	}
	return value
}

func (a *RuntimeAccount) update(value config.Account) {
	a.mu.Lock()
	a.value = value
	a.mu.Unlock()
}

func (a *RuntimeAccount) IdentityHeaders() (userID, email string) {
	value := a.Snapshot()
	return value.UserID, value.Email
}

func (a *RuntimeAccount) Available(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.value.Enabled && !now.Before(a.cooldownUntil)
}

func (a *RuntimeAccount) Acquire()        { a.inFlight.Add(1) }
func (a *RuntimeAccount) Release()        { a.inFlight.Add(-1) }
func (a *RuntimeAccount) InFlight() int64 { return a.inFlight.Load() }

func (a *RuntimeAccount) MarkCooldown(duration time.Duration) {
	if duration <= 0 {
		duration = 5 * time.Minute
	}
	a.MarkCooldownUntil(time.Now().Add(duration))
}

func (a *RuntimeAccount) MarkCooldownUntil(until time.Time) {
	a.mu.Lock()
	if until.After(a.cooldownUntil) {
		a.cooldownUntil = until
	}
	a.mu.Unlock()
}

func (a *RuntimeAccount) ClearCooldown() {
	a.mu.Lock()
	a.cooldownUntil = time.Time{}
	a.mu.Unlock()
}

func (a *RuntimeAccount) CooldownUntil() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cooldownUntil
}

func (a *RuntimeAccount) Token(ctx context.Context, force bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !force && a.value.AccessToken != "" && (a.value.ExpiresAt.IsZero() || time.Now().Add(2*time.Minute).Before(a.value.ExpiresAt)) {
		return a.value.AccessToken, nil
	}
	// API Key accounts (or any account without refresh) never refresh.
	if a.value.RefreshToken == "" || a.value.Kind == config.AccountKindAPIKey {
		if a.value.AccessToken != "" {
			return a.value.AccessToken, nil
		}
		return "", errors.New("账号缺少可用凭据")
	}
	tokens, err := a.oauth.Refresh(ctx, a.value.RefreshToken)
	if err != nil {
		return "", err
	}
	a.value.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		a.value.RefreshToken = tokens.RefreshToken
	}
	a.value.ExpiresAt = tokens.ExpiresAt
	if err := a.store.UpdateTokens(config.AccountIdentity(a.value), a.value.AccessToken, a.value.RefreshToken, a.value.ExpiresAt); err != nil {
		return "", err
	}
	return a.value.AccessToken, nil
}

type Pool struct {
	mu        sync.Mutex
	accounts  []*RuntimeAccount
	sessions  map[string]*RuntimeAccount
	round     uint64
	preferred string
	store     *config.Store
	oauth     OAuthClient
}

func NewPool(store *config.Store, oauth OAuthClient) *Pool {
	pool := &Pool{store: store, oauth: oauth, sessions: make(map[string]*RuntimeAccount)}
	pool.Reload()
	return pool
}

func (p *Pool) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg := p.store.Snapshot()
	old := make(map[string]*RuntimeAccount, len(p.accounts))
	for _, value := range p.accounts {
		old[value.Identity()] = value
	}
	next := make([]*RuntimeAccount, 0, len(cfg.Accounts))
	present := make(map[*RuntimeAccount]bool, len(cfg.Accounts))
	for _, value := range cfg.Accounts {
		identity := config.AccountIdentity(value)
		runtimeAccount := old[identity]
		if runtimeAccount == nil {
			runtimeAccount = &RuntimeAccount{value: value, store: p.store, oauth: p.oauth}
		} else {
			runtimeAccount.update(value)
		}
		next = append(next, runtimeAccount)
		present[runtimeAccount] = value.Enabled
	}
	p.accounts = next
	p.preferred = cfg.PreferredAccount
	for session, value := range p.sessions {
		if !present[value] {
			delete(p.sessions, session)
		}
	}
}

func (p *Pool) Select(session string) (*RuntimeAccount, bool, error) {
	return p.selectAccount(session, nil)
}

func (p *Pool) SelectNext(session string, exclude *RuntimeAccount) (*RuntimeAccount, bool, error) {
	return p.selectAccount(session, exclude)
}

func (p *Pool) selectAccount(session string, exclude *RuntimeAccount) (*RuntimeAccount, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if session != "" {
		if value := p.sessions[session]; value != nil {
			if value != exclude && value.Available(now) {
				value.Acquire()
				return value, true, nil
			}
			delete(p.sessions, session)
		}
	}
	available := make([]*RuntimeAccount, 0, len(p.accounts))
	for _, value := range p.accounts {
		if value != exclude && value.Available(now) {
			available = append(available, value)
		}
	}
	if len(available) == 0 {
		return nil, false, ErrNoAvailableAccount
	}
	var selected *RuntimeAccount
	if p.preferred != "" {
		for _, value := range available {
			if value.Identity() == p.preferred {
				selected = value
				break
			}
		}
	}
	if selected == nil && session != "" {
		hasher := fnv.New64a()
		_, _ = hasher.Write([]byte(session))
		selected = available[hasher.Sum64()%uint64(len(available))]
	}
	if selected == nil {
		selected = available[p.round%uint64(len(available))]
		p.round++
	}
	if session != "" {
		p.sessions[session] = selected
	}
	selected.Acquire()
	return selected, false, nil
}

func (p *Pool) SetPreferred(identity string) error {
	if err := p.store.SetPreferredAccount(identity); err != nil {
		return err
	}
	p.mu.Lock()
	p.preferred = identity
	p.sessions = make(map[string]*RuntimeAccount)
	p.mu.Unlock()
	return nil
}

func (p *Pool) Evict(session string, value *RuntimeAccount) {
	if session == "" {
		return
	}
	p.mu.Lock()
	if p.sessions[session] == value {
		delete(p.sessions, session)
	}
	p.mu.Unlock()
}

func (p *Pool) Account(identity string) (*RuntimeAccount, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, value := range p.accounts {
		if value.Identity() == identity {
			return value, true
		}
	}
	return nil, false
}

func (p *Pool) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accounts)
}

type Status struct {
	Identity      string        `json:"identity"`
	Name          string        `json:"name"`
	Kind          string        `json:"kind,omitempty"`
	Email         string        `json:"email,omitempty"`
	Enabled       bool          `json:"enabled"`
	Available     bool          `json:"available"`
	Preferred     bool          `json:"preferred"`
	InFlight      int64         `json:"in_flight"`
	ExpiresAt     time.Time     `json:"expires_at"`
	CooldownUntil time.Time     `json:"cooldown_until,omitempty"`
	Usage         *config.Usage `json:"usage,omitempty"`
}

func (p *Pool) Status() []Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	result := make([]Status, 0, len(p.accounts))
	for _, runtimeAccount := range p.accounts {
		value := runtimeAccount.Snapshot()
		identity := config.AccountIdentity(value)
		kind := value.Kind
		if kind == "" {
			kind = config.AccountKindOAuth
		}
		result = append(result, Status{
			Identity: identity, Name: value.Name, Kind: kind, Email: value.Email, Enabled: value.Enabled,
			Available: runtimeAccount.Available(now), Preferred: identity == p.preferred,
			InFlight: runtimeAccount.InFlight(), ExpiresAt: value.ExpiresAt,
			CooldownUntil: runtimeAccount.CooldownUntil(), Usage: value.Usage,
		})
	}
	return result
}
