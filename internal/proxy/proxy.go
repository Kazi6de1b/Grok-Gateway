package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"grok-gateway/internal/account"
	"grok-gateway/internal/config"
	"grok-gateway/internal/observe"
)

const maxRequestBody = 64 << 20

type Handler struct {
	pool      *account.Pool
	client    *http.Client
	upstream  *url.URL
	cooldown  time.Duration
	logger    *slog.Logger
	startedAt time.Time
	localBase string
	store     *config.Store
	observe   *observe.Store
}

func NewHandler(cfg config.Config, pool *account.Pool, client *http.Client, logger *slog.Logger, store *config.Store, obs *observe.Store) (*Handler, error) {
	upstream, err := url.Parse(strings.TrimRight(cfg.UpstreamBaseURL, "/"))
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		pool: pool, client: client, upstream: upstream, cooldown: cfg.CooldownDuration(),
		logger: logger, startedAt: time.Now(), localBase: "http://" + cfg.Listen + "/v1",
		store: store, observe: obs,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		h.health(w)
		return
	}
	if r.URL.Path == "/" {
		writeJSON(w, http.StatusOK, map[string]any{
			"name": "Grok Gateway", "status": "running", "grok_base_url": h.localBase,
		})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1" {
		writeError(w, http.StatusNotFound, "仅支持 Grok Build 原生 /v1/* 路径")
		return
	}
	if !h.authorizeGateway(w, r) {
		return
	}
	h.forward(w, r)
}

func (h *Handler) authorizeGateway(w http.ResponseWriter, r *http.Request) bool {
	if h.store == nil {
		return true
	}
	cfg := h.store.Snapshot()
	if !cfg.RequireAPIKey || strings.TrimSpace(cfg.GatewayAPIKey) == "" {
		return true
	}
	provided := extractClientKey(r)
	if provided == "" || provided != cfg.GatewayAPIKey {
		writeError(w, http.StatusUnauthorized, "需要有效的网关 API Key（Authorization: Bearer <key> 或 x-api-key）")
		return false
	}
	return true
}

func extractClientKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func (h *Handler) health(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "uptime_seconds": int64(time.Since(h.startedAt).Seconds()), "accounts": h.pool.Status(),
	})
}

func (h *Handler) forward(w http.ResponseWriter, incoming *http.Request) {
	started := time.Now()
	body, err := io.ReadAll(io.LimitReader(incoming.Body, maxRequestBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取请求失败")
		return
	}
	if len(body) > maxRequestBody {
		writeError(w, http.StatusRequestEntityTooLarge, "请求体超过 64 MiB")
		return
	}
	requestModel := observe.ExtractModelFromRequest(body)
	session := sessionKey(incoming, body)
	var previous *account.RuntimeAccount
	for attempt := 0; attempt < h.pool.Count(); attempt++ {
		selected, pinned, selectErr := h.pool.SelectNext(session, previous)
		if selectErr != nil {
			h.record(incoming, body, requestModel, nil, http.StatusTooManyRequests, time.Since(started), nil, "所有 Grok Build 账号当前均不可用或正在冷却", "no_available_account")
			writeError(w, http.StatusTooManyRequests, "所有 Grok Build 账号当前均不可用或正在冷却")
			return
		}
		response, requestErr := h.do(incoming.Context(), incoming, body, selected, false)
		if requestErr != nil {
			selected.Release()
			h.logger.Error("upstream_request_failed", "account", selected.Name(), "error", requestErr)
			h.record(incoming, body, requestModel, selected, http.StatusBadGateway, time.Since(started), nil, requestErr.Error(), "upstream_error")
			writeError(w, http.StatusBadGateway, "Grok Build 上游请求失败: "+requestErr.Error())
			return
		}
		if response.StatusCode == http.StatusUnauthorized {
			_ = response.Body.Close()
			response, requestErr = h.do(incoming.Context(), incoming, body, selected, true)
			if requestErr != nil {
				selected.MarkCooldown(h.cooldown)
				h.pool.Evict(session, selected)
				selected.Release()
				previous = selected
				continue
			}
			if response.StatusCode == http.StatusUnauthorized {
				_ = response.Body.Close()
				selected.MarkCooldown(h.cooldown)
				h.pool.Evict(session, selected)
				selected.Release()
				previous = selected
				h.logger.Warn("account_rejected_after_refresh", "account", selected.Name())
				continue
			}
		}
		if response.StatusCode == http.StatusTooManyRequests {
			duration := account.ParseRetryAfter(response.Header.Get("Retry-After"))
			if duration == 0 {
				duration = h.cooldown
			}
			_ = response.Body.Close()
			selected.MarkCooldown(duration)
			h.pool.Evict(session, selected)
			selected.Release()
			previous = selected
			h.logger.Warn("account_cooldown_and_failover", "account", selected.Name(), "duration", duration, "was_pinned", pinned)
			continue
		}
		defer selected.Release()
		defer response.Body.Close()

		var capture bytes.Buffer
		tee := io.TeeReader(response.Body, &limitedBuffer{buf: &capture, remain: 2 << 20})
		copyHeaders(w.Header(), response.Header)
		// Surface a clearer message for common free-tier / model mistakes.
		if response.StatusCode == http.StatusPaymentRequired {
			w.Header().Set("X-Gateway-Hint", "402 多为模型/产品路径或 credits 问题；Grok Build 请用 /v1/responses + 账号可用模型（如 grok-4.5）")
		}
		w.WriteHeader(response.StatusCode)
		if err := streamCopy(w, tee); err != nil && !errors.Is(err, context.Canceled) {
			h.logger.Warn("downstream_stream_ended", "error", err)
		}
		h.record(incoming, body, requestModel, selected, response.StatusCode, time.Since(started), capture.Bytes(), "", "")
		return
	}
	h.record(incoming, body, requestModel, nil, http.StatusTooManyRequests, time.Since(started), nil, "所有 Grok Build 账号额度均已用尽或正在冷却", "all_exhausted")
	writeError(w, http.StatusTooManyRequests, "所有 Grok Build 账号额度均已用尽或正在冷却")
}

func (h *Handler) record(incoming *http.Request, body []byte, requestModel string, selected *account.RuntimeAccount, status int, elapsed time.Duration, responseBody []byte, errMsg, errCode string) {
	if h.observe == nil {
		return
	}
	entry := observe.RequestLog{
		Time: time.Now().UTC(), Method: incoming.Method, Path: incoming.URL.Path,
		Model: requestModel, Status: status, DurationMs: elapsed.Milliseconds(),
		Error: errMsg, ErrorCode: errCode,
	}
	if selected != nil {
		entry.Account = selected.Identity()
		entry.AccountName = selected.Name()
	}
	if len(responseBody) > 0 {
		in, out, cached, total, model, code, msg := observe.ParseUsageFromBody(responseBody)
		entry.InputTokens, entry.OutputTokens, entry.CachedTokens, entry.TotalTokens = in, out, cached, total
		if model != "" {
			entry.Model = model
		}
		if code != "" {
			entry.ErrorCode = code
		}
		if msg != "" && entry.Error == "" {
			entry.Error = msg
		}
	}
	if entry.Error == "" && status >= 400 && len(responseBody) > 0 {
		_, _, _, _, _, code, msg := observe.ParseUsageFromBody(responseBody)
		if code != "" {
			entry.ErrorCode = code
		}
		if msg != "" {
			entry.Error = truncate(msg, 240)
		}
	}
	if entry.Model == "" {
		entry.Model = requestModel
	}
	if h.store != nil && h.store.Snapshot().LogRequestBodies && len(body) > 0 {
		// Keep a tiny preview only when explicitly enabled.
		preview := truncate(string(body), 200)
		if entry.Error == "" {
			entry.Error = "body:" + preview
		}
	}
	h.observe.Record(entry)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

func (h *Handler) do(ctx context.Context, incoming *http.Request, body []byte, selected *account.RuntimeAccount, forceRefresh bool) (*http.Response, error) {
	token, err := selected.Token(ctx, forceRefresh)
	if err != nil {
		return nil, fmt.Errorf("获取账号 Token: %w", err)
	}
	target := *h.upstream
	suffix := strings.TrimPrefix(incoming.URL.Path, "/v1")
	target.Path = strings.TrimRight(h.upstream.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
	target.RawQuery = incoming.URL.RawQuery
	request, err := http.NewRequestWithContext(ctx, incoming.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header = incoming.Header.Clone()
	stripHopHeaders(request.Header)
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("x-api-key")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	setDefault(request.Header, "x-grok-client-version", "0.2.101")
	setDefault(request.Header, "x-grok-client-identifier", "grok-shell")
	setDefault(request.Header, "x-grok-client-mode", "headless")
	setDefault(request.Header, "User-Agent", "grok-shell/0.2.101 (windows; amd64)")
	userID, email := selected.IdentityHeaders()
	if userID != "" {
		request.Header.Set("x-grok-user-id", userID)
		request.Header.Set("x-userid", userID)
	} else {
		request.Header.Del("x-grok-user-id")
		request.Header.Del("x-userid")
	}
	if email != "" {
		request.Header.Set("x-email", email)
	} else {
		request.Header.Del("x-email")
	}
	return h.client.Do(request)
}

func sessionKey(request *http.Request, body []byte) string {
	for _, name := range []string{"x-grok-session-id", "x-grok-conv-id"} {
		if value := strings.TrimSpace(request.Header.Get(name)); value != "" {
			return name + ":" + value
		}
	}
	if len(body) > 0 && strings.Contains(request.Header.Get("Content-Type"), "json") {
		var payload struct {
			PromptCacheKey     string `json:"prompt_cache_key"`
			PreviousResponseID string `json:"previous_response_id"`
		}
		if json.Unmarshal(body, &payload) == nil {
			if payload.PromptCacheKey != "" {
				return "prompt:" + payload.PromptCacheKey
			}
			if payload.PreviousResponseID != "" {
				return "response:" + payload.PreviousResponseID
			}
		}
	}
	return ""
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		if isHopHeader(key) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func stripHopHeaders(header http.Header) {
	for key := range header {
		if isHopHeader(key) {
			header.Del(key)
		}
	}
}

func isHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func setDefault(header http.Header, name, value string) {
	if header.Get(name) == "" {
		header.Set(name, value)
	}
}

// limitedBuffer captures up to remain bytes for usage parsing without unbounded memory.
type limitedBuffer struct {
	buf    *bytes.Buffer
	remain int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.remain > 0 {
		chunk := p
		if len(chunk) > l.remain {
			chunk = chunk[:l.remain]
		}
		n, err := l.buf.Write(chunk)
		l.remain -= n
		if err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

func streamCopy(writer http.ResponseWriter, reader io.Reader) error {
	buffer := make([]byte, 32<<10)
	controller := http.NewResponseController(writer)
	for {
		count, readErr := reader.Read(buffer)
		if count > 0 {
			if _, err := writer.Write(buffer[:count]); err != nil {
				return err
			}
			_ = controller.Flush()
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"type": "gateway_error", "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
