package admin

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"grok-gateway/internal/account"
	"grok-gateway/internal/config"
)

//go:embed static/*
var staticFiles embed.FS

type oauthSession struct {
	authorization account.DeviceAuthorization
	nextPoll      time.Time
	expiresAt     time.Time
}

type Handler struct {
	proxy           http.Handler
	store           *config.Store
	pool            *account.Pool
	client          *http.Client
	oauth           account.OAuthClient
	logger          *slog.Logger
	version         string
	runtimeListen   string
	runtimeUpstream string
	static          http.Handler
	oauthMu         sync.Mutex
	sessions        map[string]*oauthSession
}

func NewHandler(proxy http.Handler, store *config.Store, pool *account.Pool, client *http.Client, oauth account.OAuthClient, version string, logger *slog.Logger) (*Handler, error) {
	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg := store.Snapshot()
	return &Handler{
		proxy: proxy, store: store, pool: pool, client: client, oauth: oauth, logger: logger,
		version: version, runtimeListen: cfg.Listen, runtimeUpstream: cfg.UpstreamBaseURL,
		static: http.FileServer(http.FS(assets)), sessions: make(map[string]*oauthSession),
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		h.serveIndex(w, r)
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		clone := r.Clone(r.Context())
		clone.URL.Path = strings.TrimPrefix(r.URL.Path, "/assets")
		h.static.ServeHTTP(w, clone)
	case r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/v1"):
		h.proxy.ServeHTTP(w, r)
	case r.URL.Path == "/api/state" && r.Method == http.MethodGet:
		h.state(w)
	case r.URL.Path == "/api/oauth/start" && r.Method == http.MethodPost:
		h.startOAuth(w, r)
	case r.URL.Path == "/api/oauth/poll" && r.Method == http.MethodPost:
		h.pollOAuth(w, r)
	case r.URL.Path == "/api/accounts/preferred" && r.Method == http.MethodPost:
		h.setPreferred(w, r)
	case r.URL.Path == "/api/accounts/enabled" && r.Method == http.MethodPost:
		h.setEnabled(w, r)
	case r.URL.Path == "/api/accounts/delete" && r.Method == http.MethodPost:
		h.deleteAccount(w, r)
	case r.URL.Path == "/api/accounts/usage" && r.Method == http.MethodPost:
		h.refreshUsage(w, r, false)
	case r.URL.Path == "/api/accounts/usage-all" && r.Method == http.MethodPost:
		h.refreshUsage(w, r, true)
	case r.URL.Path == "/api/accounts/cooldown/clear" && r.Method == http.MethodPost:
		h.clearCooldown(w, r)
	case r.URL.Path == "/api/settings" && r.Method == http.MethodPut:
		h.updateSettings(w, r)
	case r.URL.Path == "/api/grok/launch" && r.Method == http.MethodPost:
		h.launchGrok(w)
	default:
		writeError(w, http.StatusNotFound, "接口不存在")
	}
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (h *Handler) state(w http.ResponseWriter) {
	cfg := h.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"version": h.version,
		"settings": map[string]any{
			"listen": cfg.Listen, "upstream_base_url": cfg.UpstreamBaseURL,
			"outbound_proxy": cfg.OutboundProxy, "cooldown": cfg.Cooldown,
		},
		"grok_base_url": "http://" + h.runtimeListen + "/v1",
		"accounts":      h.pool.Status(),
	})
}

func (h *Handler) startOAuth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	authorization, err := h.oauth.StartDevice(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "启动 OAuth 失败，请检查出站代理："+err.Error())
		return
	}
	id, err := randomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.oauthMu.Lock()
	h.sessions[id] = &oauthSession{
		authorization: authorization, nextPoll: time.Now().Add(authorization.Interval),
		expiresAt: time.Now().Add(authorization.ExpiresIn),
	}
	h.oauthMu.Unlock()
	verificationURL := authorization.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = authorization.VerificationURI
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": id, "user_code": authorization.UserCode, "verification_url": verificationURL,
		"interval_ms": authorization.Interval.Milliseconds(), "expires_in_seconds": int64(authorization.ExpiresIn.Seconds()),
	})
}

func (h *Handler) pollOAuth(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	h.oauthMu.Lock()
	session := h.sessions[input.SessionID]
	if session == nil {
		h.oauthMu.Unlock()
		writeError(w, http.StatusNotFound, "OAuth 会话不存在或已过期")
		return
	}
	if time.Now().After(session.expiresAt) {
		delete(h.sessions, input.SessionID)
		h.oauthMu.Unlock()
		writeError(w, http.StatusGone, "OAuth 会话已过期")
		return
	}
	if time.Now().Before(session.nextPoll) {
		retry := time.Until(session.nextPoll).Milliseconds()
		h.oauthMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "retry_after_ms": retry})
		return
	}
	session.nextPoll = time.Now().Add(session.authorization.Interval)
	h.oauthMu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tokens, err := h.oauth.PollDevice(ctx, session.authorization.DeviceCode)
	if errors.Is(err, account.ErrAuthorizationPending) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "retry_after_ms": session.authorization.Interval.Milliseconds()})
		return
	}
	if errors.Is(err, account.ErrSlowDown) {
		h.oauthMu.Lock()
		session.nextPoll = time.Now().Add(session.authorization.Interval + 5*time.Second)
		h.oauthMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "retry_after_ms": (session.authorization.Interval + 5*time.Second).Milliseconds()})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	value := account.AccountFromTokens("", tokens)
	if err := h.store.UpsertAccount(value); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.oauthMu.Lock()
	delete(h.sessions, input.SessionID)
	h.oauthMu.Unlock()
	h.pool.Reload()
	writeJSON(w, http.StatusOK, map[string]any{"status": "complete", "account": value.Name})
}

func (h *Handler) setPreferred(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityInput(w, r)
	if !ok {
		return
	}
	if err := h.pool.SetPreferred(identity); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.state(w)
}

func (h *Handler) setEnabled(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Identity string `json:"identity"`
		Enabled  bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.store.SetAccountEnabled(input.Identity, input.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.pool.Reload()
	h.state(w)
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityInput(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteAccount(identity); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.pool.Reload()
	h.state(w)
}

func (h *Handler) refreshUsage(w http.ResponseWriter, r *http.Request, all bool) {
	identities := make([]string, 0)
	if all {
		for _, value := range h.pool.Status() {
			if value.Enabled {
				identities = append(identities, value.Identity)
			}
		}
	} else {
		identity, ok := identityInput(w, r)
		if !ok {
			return
		}
		identities = append(identities, identity)
	}
	errorsByAccount := make(map[string]string)
	for _, identity := range identities {
		if err := h.syncUsage(r.Context(), identity); err != nil {
			errorsByAccount[identity] = err.Error()
		}
	}
	h.pool.Reload()
	if len(errorsByAccount) > 0 {
		writeJSON(w, http.StatusMultiStatus, map[string]any{"state": h.stateValue(), "errors": errorsByAccount})
		return
	}
	writeJSON(w, http.StatusOK, h.stateValue())
}

func (h *Handler) syncUsage(ctx context.Context, identity string) error {
	runtimeAccount, ok := h.pool.Account(identity)
	if !ok {
		return errors.New("账号不存在")
	}
	cfg := h.store.Snapshot()
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	usage, err := account.FetchUsage(requestCtx, runtimeAccount, h.client, h.runtimeUpstream)
	if err != nil {
		return err
	}
	if err := h.store.UpdateUsage(identity, usage); err != nil {
		return err
	}
	if usage.UsagePercent >= 100 {
		if resetAt, ok := account.UsageResetAt(usage); ok && resetAt.After(time.Now()) {
			runtimeAccount.MarkCooldownUntil(resetAt)
		} else {
			runtimeAccount.MarkCooldown(cfg.CooldownDuration())
		}
	} else {
		runtimeAccount.ClearCooldown()
	}
	return nil
}

func (h *Handler) clearCooldown(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityInput(w, r)
	if !ok {
		return
	}
	value, exists := h.pool.Account(identity)
	if !exists {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	value.ClearCooldown()
	h.state(w)
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Listen          string `json:"listen"`
		UpstreamBaseURL string `json:"upstream_base_url"`
		OutboundProxy   string `json:"outbound_proxy"`
		Cooldown        string `json:"cooldown"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.store.UpdateSettings(input.Listen, input.UpstreamBaseURL, input.OutboundProxy, input.Cooldown); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true, "restart_required": true})
}

func (h *Handler) launchGrok(w http.ResponseWriter) {
	hasEnabled := false
	for _, value := range h.pool.Status() {
		if value.Enabled {
			hasEnabled = true
			break
		}
	}
	if !hasEnabled {
		writeError(w, http.StatusBadRequest, "请先添加至少一个账号")
		return
	}
	baseURL := "http://" + h.runtimeListen + "/v1"
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		launcher := filepath.Join(filepath.Dir(h.store.Path()), "启动-Grok-Build.cmd")
		content := "@echo off\r\nset \"GROK_CLI_CHAT_PROXY_BASE_URL=" + baseURL + "\"\r\ngrok\r\n"
		if err := os.WriteFile(launcher, []byte(content), 0o600); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		command = exec.Command("cmd.exe", "/c", "start", "", launcher)
	} else {
		command = exec.Command("sh", "-c", "GROK_CLI_CHAT_PROXY_BASE_URL='"+strings.ReplaceAll(baseURL, "'", "")+"' grok")
	}
	if err := command.Start(); err != nil {
		writeError(w, http.StatusInternalServerError, "启动 Grok Build 失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"launched": true, "base_url": baseURL})
}

func (h *Handler) stateValue() map[string]any {
	cfg := h.store.Snapshot()
	return map[string]any{
		"version": h.version,
		"settings": map[string]any{
			"listen": cfg.Listen, "upstream_base_url": cfg.UpstreamBaseURL,
			"outbound_proxy": cfg.OutboundProxy, "cooldown": cfg.Cooldown,
		},
		"grok_base_url": "http://" + h.runtimeListen + "/v1", "accounts": h.pool.Status(),
	}
}

func identityInput(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Identity string `json:"identity"`
	}
	if !decodeJSON(w, r, &input) {
		return "", false
	}
	if strings.TrimSpace(input.Identity) == "" {
		writeError(w, http.StatusBadRequest, "缺少账号 identity")
		return "", false
	}
	return input.Identity, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, "请求 JSON 无效："+err.Error())
		return false
	}
	return true
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
