package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMChat_ErrorHandling(t *testing.T) {
	// Test Case 1: API Error (500)
	t.Run("API Error 500", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Internal Server Error"))
		}))
		defer server.Close()

		messages := []LLMMessage{{Role: "user", Content: "hello"}}
		// Use the mock server URL as apiBase
		_, err := llmChat(context.Background(), messages, "test-model", 0, nil, nil, "test-key", server.URL, false, nil, false)

		if err == nil {
			t.Fatal("Expected error for 500 response, got nil")
		}
	})

	// Test Case 2: Empty Choices (200)
	t.Run("Empty Choices 200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			// Return valid JSON but empty choices
			resp := map[string]interface{}{
				"choices": []interface{}{},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		messages := []LLMMessage{{Role: "user", Content: "hello"}}
		_, err := llmChat(context.Background(), messages, "test-model", 0, nil, nil, "test-key", server.URL, false, nil, false)

		if err == nil {
			t.Fatal("Expected error for empty choices, got nil")
		}
	})
}
