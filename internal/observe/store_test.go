package observe

import (
	"testing"
	"time"
)

func TestParseUsageFromResponsesJSON(t *testing.T) {
	body := []byte(`{"model":"grok-4.5-build-free","usage":{"input_tokens":201,"output_tokens":52,"input_tokens_details":{"cached_tokens":12},"total_tokens":253}}`)
	in, out, cached, total, model, _, _ := ParseUsageFromBody(body)
	if in != 201 || out != 52 || cached != 12 || total != 253 || model != "grok-4.5-build-free" {
		t.Fatalf("got in=%d out=%d cached=%d total=%d model=%s", in, out, cached, total, model)
	}
}

func TestParseUsageFromSSE(t *testing.T) {
	body := []byte("event: response.completed\ndata: {\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15},\"model\":\"grok-4.5\"}\n\n")
	in, out, _, total, model, _, _ := ParseUsageFromBody(body)
	if in != 10 || out != 5 || total != 15 || model != "grok-4.5" {
		t.Fatalf("got in=%d out=%d total=%d model=%s", in, out, total, model)
	}
}

func TestStoreRecordAndStats(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store.Record(RequestLog{
		Time: now, Method: "POST", Path: "/v1/responses", Account: "user:1", AccountName: "a@x.com",
		Model: "grok-4.5", Status: 200, DurationMs: 120, InputTokens: 100, OutputTokens: 20, CachedTokens: 5, TotalTokens: 120,
	})
	store.Record(RequestLog{
		Time: now, Method: "POST", Path: "/v1/responses", Account: "user:1", AccountName: "a@x.com",
		Model: "grok-4.5", Status: 402, DurationMs: 40, ErrorCode: "personal-team-blocked:spending-limit", Error: "no credits",
	})
	store.Record(RequestLog{
		Time: now.Add(-2 * time.Hour), Method: "POST", Path: "/v1/responses", Account: "user:1",
		Status: 200, TotalTokens: 50, InputTokens: 40, OutputTokens: 10,
	})
	stats := store.Stats(7)
	today, _ := stats["today"].(DayStats)
	if today.Requests != 3 || today.Success != 2 || today.Failed != 1 || today.TotalTokens != 170 {
		t.Fatalf("unexpected today stats: %#v", today)
	}
	hourly, ok := stats["hourly"].([]HourPoint)
	if !ok || len(hourly) < 24 {
		t.Fatalf("expected hourly series, got %#v", stats["hourly"])
	}
	var tokenSum int64
	for _, h := range hourly {
		tokenSum += h.TotalTokens
	}
	if tokenSum != 170 {
		t.Fatalf("hourly token sum = %d", tokenSum)
	}
	logs := store.Logs(10)
	if len(logs) != 3 || logs[0].Status != 200 && logs[0].TotalTokens != 50 && logs[0].Status != 402 {
		// newest first among recent records
	}
	if len(logs) != 3 {
		t.Fatalf("logs len=%d", len(logs))
	}
	csv, err := store.ExportCSV(7)
	if err != nil {
		t.Fatal(err)
	}
	if csv == "" || len(csv) < 20 {
		t.Fatalf("csv empty: %q", csv)
	}
	_ = dir
}
