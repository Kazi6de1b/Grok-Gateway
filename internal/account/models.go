package account

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"grok-gateway/internal/observe"
)

func FetchModels(ctx context.Context, runtimeAccount *RuntimeAccount, client *http.Client, upstreamBaseURL string) ([]observe.ModelInfo, error) {
	return fetchModels(ctx, runtimeAccount, client, upstreamBaseURL, false)
}

func fetchModels(ctx context.Context, runtimeAccount *RuntimeAccount, client *http.Client, upstreamBaseURL string, forceRefresh bool) ([]observe.ModelInfo, error) {
	token, err := runtimeAccount.Token(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(upstreamBaseURL, "/") + "/models"
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	request.Header.Set("x-grok-client-version", "0.2.101")
	request.Header.Set("x-grok-client-identifier", "grok-shell")
	request.Header.Set("x-grok-client-mode", "headless")
	request.Header.Set("User-Agent", "grok-shell/0.2.101 (windows; amd64)")
	userID, email := runtimeAccount.IdentityHeaders()
	if userID != "" {
		request.Header.Set("x-userid", userID)
	}
	if email != "" {
		request.Header.Set("x-email", email)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && !forceRefresh {
		return fetchModels(ctx, runtimeAccount, client, upstreamBaseURL, true)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("models 接口返回 %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return ParseModels(body)
}

func ParseModels(data []byte) ([]observe.ModelInfo, error) {
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Model       string `json:"model"`
			Name        string `json:"name"`
			Description string `json:"description"`
			APIBackend  string `json:"api_backend"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("解析 models: %w", err)
	}
	result := make([]observe.ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := item.ID
		if id == "" {
			id = item.Model
		}
		if id == "" {
			continue
		}
		name := item.Name
		if name == "" {
			name = id
		}
		result = append(result, observe.ModelInfo{
			ID: id, Name: name, Description: item.Description, APIBackend: item.APIBackend,
		})
	}
	return result, nil
}
