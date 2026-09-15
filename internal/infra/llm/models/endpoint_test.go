package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyUsesSelectedOpenAIEndpoint(t *testing.T) {
	for _, endpoint := range []string{"chat", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			path := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("key not forwarded")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"test rejection","type":"authentication_error"}}`))
			}))
			defer server.Close()
			err := Verify(context.Background(), Config{Provider: "openai", Model: "custom", API: endpoint, APIKey: "test-key", BaseURL: server.URL + "/v1"})
			expected := "/v1/chat/completions"
			if endpoint == "responses" {
				expected = "/v1/responses"
			}
			if err == nil || path != expected {
				t.Fatalf("path=%q want=%q err=%v", path, expected, err)
			}
		})
	}
}
