package observe

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxLogs = 1000
	MaxDayKeep     = 30
)

type RequestLog struct {
	ID           string    `json:"id"`
	Time         time.Time `json:"time"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	Account      string    `json:"account,omitempty"`
	AccountName  string    `json:"account_name,omitempty"`
	Model        string    `json:"model,omitempty"`
	Status       int       `json:"status"`
	DurationMs   int64     `json:"duration_ms"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CachedTokens int64     `json:"cached_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
	Error        string    `json:"error,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
}

type AccountStats struct {
	Identity     string `json:"identity"`
	Name         string `json:"name,omitempty"`
	Requests     int    `json:"requests"`
	Success      int    `json:"success"`
	Failed       int    `json:"failed"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CachedTokens int64  `json:"cached_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

type DayStats struct {
	Date         string         `json:"date"`
	Requests     int            `json:"requests"`
	Success      int            `json:"success"`
	Failed       int            `json:"failed"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	CachedTokens int64          `json:"cached_tokens"`
	TotalTokens  int64          `json:"total_tokens"`
	ByAccount    []AccountStats `json:"by_account,omitempty"`
}

type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	APIBackend  string `json:"api_backend,omitempty"`
}

type ModelsCache struct {
	Models    []ModelInfo `json:"models"`
	FetchedAt time.Time   `json:"fetched_at"`
	Error     string      `json:"error,omitempty"`
}

type Store struct {
	mu          sync.Mutex
	logs        []RequestLog
	maxLogs     int
	days        map[string]*dayBucket
	dir         string
	jsonlPath   string
	models      map[string]ModelsCache
	modelsTTL   time.Duration
	seq         uint64
}

type dayBucket struct {
	stats     DayStats
	byAccount map[string]*AccountStats
}

func New(dir string) (*Store, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{
		maxLogs:   DefaultMaxLogs,
		days:      make(map[string]*dayBucket),
		dir:       dir,
		jsonlPath: filepath.Join(dir, "requests.jsonl"),
		models:    make(map[string]ModelsCache),
		modelsTTL: 20 * time.Minute,
	}
	_ = s.loadJSONLTail(200)
	return s, nil
}

func (s *Store) Record(entry RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	s.seq++
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%d-%d", entry.Time.UnixMilli(), s.seq)
	}
	s.logs = append(s.logs, entry)
	if len(s.logs) > s.maxLogs {
		s.logs = append([]RequestLog(nil), s.logs[len(s.logs)-s.maxLogs:]...)
	}
	s.applyDayLocked(entry)
	s.pruneDaysLocked()
	_ = s.appendJSONL(entry)
}

func (s *Store) applyDayLocked(entry RequestLog) {
	day := entry.Time.Local().Format("2006-01-02")
	bucket := s.days[day]
	if bucket == nil {
		bucket = &dayBucket{
			stats:     DayStats{Date: day},
			byAccount: make(map[string]*AccountStats),
		}
		s.days[day] = bucket
	}
	bucket.stats.Requests++
	if entry.Status >= 200 && entry.Status < 400 {
		bucket.stats.Success++
	} else {
		bucket.stats.Failed++
	}
	bucket.stats.InputTokens += entry.InputTokens
	bucket.stats.OutputTokens += entry.OutputTokens
	bucket.stats.CachedTokens += entry.CachedTokens
	bucket.stats.TotalTokens += entry.TotalTokens

	key := entry.Account
	if key == "" {
		key = "_unknown"
	}
	acc := bucket.byAccount[key]
	if acc == nil {
		acc = &AccountStats{Identity: entry.Account, Name: entry.AccountName}
		bucket.byAccount[key] = acc
	}
	if entry.AccountName != "" {
		acc.Name = entry.AccountName
	}
	acc.Requests++
	if entry.Status >= 200 && entry.Status < 400 {
		acc.Success++
	} else {
		acc.Failed++
	}
	acc.InputTokens += entry.InputTokens
	acc.OutputTokens += entry.OutputTokens
	acc.CachedTokens += entry.CachedTokens
	acc.TotalTokens += entry.TotalTokens
}

func (s *Store) pruneDaysLocked() {
	if len(s.days) <= MaxDayKeep {
		return
	}
	keys := make([]string, 0, len(s.days))
	for k := range s.days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for len(keys) > MaxDayKeep {
		delete(s.days, keys[0])
		keys = keys[1:]
	}
}

func (s *Store) appendJSONL(entry RequestLog) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func (s *Store) loadJSONLTail(limit int) error {
	data, err := os.ReadFile(s.jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if len(lines) > limit {
		start = len(lines) - limit
	}
	for _, line := range lines[start:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry RequestLog
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		s.logs = append(s.logs, entry)
		s.applyDayLocked(entry)
	}
	if len(s.logs) > s.maxLogs {
		s.logs = append([]RequestLog(nil), s.logs[len(s.logs)-s.maxLogs:]...)
	}
	s.pruneDaysLocked()
	return nil
}

func (s *Store) Logs(limit int) []RequestLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.logs) {
		limit = len(s.logs)
	}
	out := make([]RequestLog, limit)
	copy(out, s.logs[len(s.logs)-limit:])
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *Store) ClearLogs() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = nil
	s.days = make(map[string]*dayBucket)
	if err := os.Remove(s.jsonlPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) Stats(days int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if days <= 0 {
		days = 7
	}
	if days > MaxDayKeep {
		days = MaxDayKeep
	}
	today := time.Now().Local()
	series := make([]DayStats, 0, days)
	var total DayStats
	accountTotals := map[string]*AccountStats{}

	for i := days - 1; i >= 0; i-- {
		date := today.AddDate(0, 0, -i).Format("2006-01-02")
		bucket := s.days[date]
		day := DayStats{Date: date}
		if bucket != nil {
			day = bucket.stats
			day.ByAccount = make([]AccountStats, 0, len(bucket.byAccount))
			for _, acc := range bucket.byAccount {
				day.ByAccount = append(day.ByAccount, *acc)
				agg := accountTotals[acc.Identity]
				if agg == nil {
					copyAcc := *acc
					accountTotals[acc.Identity] = &copyAcc
				} else {
					agg.Requests += acc.Requests
					agg.Success += acc.Success
					agg.Failed += acc.Failed
					agg.InputTokens += acc.InputTokens
					agg.OutputTokens += acc.OutputTokens
					agg.CachedTokens += acc.CachedTokens
					agg.TotalTokens += acc.TotalTokens
					if acc.Name != "" {
						agg.Name = acc.Name
					}
				}
			}
			sort.Slice(day.ByAccount, func(i, j int) bool {
				return day.ByAccount[i].TotalTokens > day.ByAccount[j].TotalTokens
			})
			total.Requests += day.Requests
			total.Success += day.Success
			total.Failed += day.Failed
			total.InputTokens += day.InputTokens
			total.OutputTokens += day.OutputTokens
			total.CachedTokens += day.CachedTokens
			total.TotalTokens += day.TotalTokens
		}
		series = append(series, day)
	}

	accounts := make([]AccountStats, 0, len(accountTotals))
	for _, acc := range accountTotals {
		accounts = append(accounts, *acc)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].TotalTokens > accounts[j].TotalTokens })

	todayKey := today.Format("2006-01-02")
	var todayStats DayStats
	if bucket := s.days[todayKey]; bucket != nil {
		todayStats = bucket.stats
	} else {
		todayStats = DayStats{Date: todayKey}
	}

	return map[string]any{
		"days":     days,
		"today":    todayStats,
		"series":   series,
		"total":    total,
		"accounts": accounts,
		"log_count": len(s.logs),
	}
}

func (s *Store) ExportCSV(days int) (string, error) {
	stats := s.Stats(days)
	series, _ := stats["series"].([]DayStats)
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"date", "requests", "success", "failed", "input_tokens", "output_tokens", "cached_tokens", "total_tokens"})
	for _, day := range series {
		_ = w.Write([]string{
			day.Date,
			fmt.Sprintf("%d", day.Requests),
			fmt.Sprintf("%d", day.Success),
			fmt.Sprintf("%d", day.Failed),
			fmt.Sprintf("%d", day.InputTokens),
			fmt.Sprintf("%d", day.OutputTokens),
			fmt.Sprintf("%d", day.CachedTokens),
			fmt.Sprintf("%d", day.TotalTokens),
		})
	}
	w.Flush()
	return b.String(), w.Error()
}

func (s *Store) GetModels(identity string) (ModelsCache, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cache, ok := s.models[identity]
	if !ok {
		return ModelsCache{}, false
	}
	if time.Since(cache.FetchedAt) > s.modelsTTL {
		return cache, false
	}
	return cache, true
}

func (s *Store) SetModels(identity string, models []ModelInfo, fetchErr string) ModelsCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	cache := ModelsCache{Models: models, FetchedAt: time.Now().UTC(), Error: fetchErr}
	s.models[identity] = cache
	return cache
}

func (s *Store) ModelsSnapshot() map[string]ModelsCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ModelsCache, len(s.models))
	for k, v := range s.models {
		out[k] = v
	}
	return out
}

// ParseUsageFromBody extracts token usage from JSON or SSE response bodies.
func ParseUsageFromBody(body []byte) (input, output, cached, total int64, model, errCode, errMsg string) {
	if len(body) == 0 {
		return
	}
	text := string(body)
	// SSE: scan data lines for JSON objects containing usage
	if strings.Contains(text, "data:") {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			in, out, cache, tot, mod, code, msg := parseUsageJSON([]byte(payload))
			if tot > total {
				input, output, cached, total = in, out, cache, tot
			}
			if mod != "" {
				model = mod
			}
			if code != "" {
				errCode, errMsg = code, msg
			}
		}
		if total > 0 || errCode != "" {
			return
		}
	}
	return parseUsageJSON(body)
}

func parseUsageJSON(data []byte) (input, output, cached, total int64, model, errCode, errMsg string) {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return
	}
	if m, ok := root["model"].(string); ok {
		model = m
	}
	if code, ok := root["code"].(string); ok {
		errCode = code
	}
	if errVal, ok := root["error"]; ok {
		switch typed := errVal.(type) {
		case string:
			errMsg = typed
		case map[string]any:
			if msg, ok := typed["message"].(string); ok {
				errMsg = msg
			}
			if code, ok := typed["code"].(string); ok && errCode == "" {
				errCode = code
			}
		}
	}
	usage := firstMap(root, "usage")
	if usage == nil {
		if resp := firstMap(root, "response"); resp != nil {
			usage = firstMap(resp, "usage")
			if m, ok := resp["model"].(string); ok && model == "" {
				model = m
			}
		}
	}
	if usage == nil {
		return
	}
	input = int64(number(usage["input_tokens"]))
	if input == 0 {
		input = int64(number(usage["prompt_tokens"]))
	}
	output = int64(number(usage["output_tokens"]))
	if output == 0 {
		output = int64(number(usage["completion_tokens"]))
	}
	total = int64(number(usage["total_tokens"]))
	if details := firstMap(usage, "input_tokens_details"); details != nil {
		cached = int64(number(details["cached_tokens"]))
	}
	if cached == 0 {
		cached = int64(number(usage["cached_tokens"]))
	}
	if total == 0 {
		total = input + output
	}
	return
}

func firstMap(root map[string]any, key string) map[string]any {
	if value, ok := root[key].(map[string]any); ok {
		return value
	}
	return nil
}

func number(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case json.Number:
		v, _ := typed.Float64()
		return v
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}

// ExtractModelFromRequest reads model field from a JSON request body.
func ExtractModelFromRequest(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return payload.Model
}
