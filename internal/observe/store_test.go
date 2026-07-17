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
	store.Record(RequestLog{
		Time: time.Now(), Method: "POST", Path: "/v1/responses", Account: "user:1", AccountName: "a@x.com",
		Model: "grok-4.5", Status: 200, DurationMs: 120, InputTokens: 100, OutputTokens: 20, CachedTokens: 5, TotalTokens: 120,
	})
	store.Record(RequestLog{
		Time: time.Now(), Method: "POST", Path: "/v1/responses", Account: "user:1", AccountName: "a@x.com",
		Model: "grok-4.5", Status: 402, DurationMs: 40, ErrorCode: "personal-team-blocked:spending-limit", Error: "no credits",
	})
	stats := store.Stats(7)
	today, _ := stats["today"].(DayStats)
	if today.Requests != 2 || today.Success != 1 || today.Failed != 1 || today.TotalTokens != 120 {
		t.Fatalf("unexpected today stats: %#v", today)
	}
	logs := store.Logs(10)
	if len(logs) != 2 || logs[0].Status != 402 {
		t.Fatalf("logs newest-first expected, got %#v", logs)
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
