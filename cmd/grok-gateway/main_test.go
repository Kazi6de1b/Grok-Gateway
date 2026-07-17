package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientUsesConfiguredProxy(t *testing.T) {
	called := false
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Host != "grok-upstream.invalid" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected proxy request URL: %s", r.URL.String())
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxyServer.Close()
	client, err := newHTTPClient(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("http://grok-upstream.invalid/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if !called || string(body) != "proxied" {
		t.Fatalf("configured proxy was not used: called=%v body=%q", called, body)
	}
}
