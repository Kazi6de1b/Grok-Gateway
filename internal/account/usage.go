package account

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"grok-gateway/internal/config"
)

func FetchUsage(ctx context.Context, runtimeAccount *RuntimeAccount, client *http.Client, upstreamBaseURL string) (config.Usage, error) {
	return fetchUsage(ctx, runtimeAccount, client, upstreamBaseURL, false)
}

func fetchUsage(ctx context.Context, runtimeAccount *RuntimeAccount, client *http.Client, upstreamBaseURL string, forceRefresh bool) (config.Usage, error) {
	token, err := runtimeAccount.Token(ctx, forceRefresh)
	if err != nil {
		return config.Usage{}, err
	}
	endpoint := strings.TrimRight(upstreamBaseURL, "/") + "/billing?format=credits"
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return config.Usage{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return config.Usage{}, err
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
		return config.Usage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && !forceRefresh {
		return fetchUsage(ctx, runtimeAccount, client, upstreamBaseURL, true)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return config.Usage{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return config.Usage{}, fmt.Errorf("Billing 接口返回 %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	usage, err := ParseUsage(body)
	if err != nil {
		return config.Usage{}, err
	}
	usage.SyncedAt = time.Now().UTC()
	return usage, nil
}

func ParseUsage(data []byte) (config.Usage, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return config.Usage{}, fmt.Errorf("解析 Billing: %w", err)
	}
	original := root
	if nested, ok := root["config"].(map[string]any); ok {
		root = nested
	}
	usage := config.Usage{
		PlanName:           planName(root, original),
		MonthlyLimit:       numberValue(firstValue(root, "monthlyLimit", "monthly_limit")),
		Used:               numberValue(firstValue(root, "used", "totalUsed", "includedUsed")),
		OnDemandCap:        numberValue(firstValue(root, "onDemandCap", "on_demand_cap", "maxAmountPerMonth")),
		OnDemandUsed:       numberValue(firstValue(root, "onDemandUsed", "on_demand_used")),
		PrepaidBalance:     numberValue(firstValue(root, "prepaidBalance", "prepaid_balance")),
		UsagePercent:       numberValue(firstValue(root, "creditUsagePercent", "credit_usage_percent")),
		BillingPeriodStart: stringValue(firstValue(root, "billingPeriodStart", "billing_period_start")),
		BillingPeriodEnd:   stringValue(firstValue(root, "billingPeriodEnd", "billing_period_end")),
	}
	if currentPeriod, ok := root["currentPeriod"].(map[string]any); ok {
		usage.UsagePeriodStart = stringValue(currentPeriod["start"])
		usage.UsagePeriodEnd = stringValue(currentPeriod["end"])
	}
	if usage.UsagePercent == 0 {
		switch {
		case usage.OnDemandCap > 0:
			usage.UsagePercent = usage.OnDemandUsed / usage.OnDemandCap * 100
		case usage.MonthlyLimit > 0:
			usage.UsagePercent = usage.Used / usage.MonthlyLimit * 100
		}
	}
	if usage.UsagePercent < 0 {
		usage.UsagePercent = 0
	}
	return usage, nil
}

// UsageResetAt returns the best reset timestamp exposed by the upstream Billing payload.
func UsageResetAt(usage config.Usage) (time.Time, bool) {
	for _, raw := range []string{usage.UsagePeriodEnd, usage.BillingPeriodEnd} {
		if raw == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339, raw)
		if err == nil {
			return value.UTC(), true
		}
	}
	return time.Time{}, false
}

func planName(values, original map[string]any) string {
	for _, source := range []map[string]any{values, original} {
		if value := stringValue(firstValue(source, "planName", "plan_name", "subscriptionName", "subscriptionTier")); value != "" {
			return value
		}
		for _, key := range []string{"plan", "subscription", "membership"} {
			switch typed := source[key].(type) {
			case string:
				if typed != "" {
					return typed
				}
			case map[string]any:
				if value := stringValue(firstValue(typed, "name", "displayName", "display_name", "label")); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case string:
		parsed, _ := strconv.ParseFloat(typed, 64)
		return parsed
	case map[string]any:
		return numberValue(typed["val"])
	default:
		return 0
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
