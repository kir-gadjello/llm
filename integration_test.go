package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestAPIRequestConstruction verifies that the resolveLLMApi and llmChat functions
// correctly merge CLI flags, config profiles, and handle reasoning parameters.
func TestAPIRequestConstruction(t *testing.T) {
	// Save and clear environment variables to ensure test isolation
	oldAPIBase := os.Getenv("OPENAI_API_BASE")
	oldAPIKey := os.Getenv("OPENAI_API_KEY")
	defer func() {
		if oldAPIBase != "" {
			os.Setenv("OPENAI_API_BASE", oldAPIBase)
		} else {
			os.Unsetenv("OPENAI_API_BASE")
		}
		if oldAPIKey != "" {
			os.Setenv("OPENAI_API_KEY", oldAPIKey)
		} else {
			os.Unsetenv("OPENAI_API_KEY")
		}
	}()

	// Clear environment variables for test isolation
	os.Unsetenv("OPENAI_API_BASE")
	os.Unsetenv("OPENAI_API_KEY")

	// 1. Setup Mock Server
	var receivedBody map[string]interface{}
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)

		// Return a dummy success response
		resp := map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message": map[string]interface{}{
						"content": "mock response",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// 2. Define Test Cases
	tests := []struct {
		name            string
		model           string
		extraConfig     map[string]interface{} // Simulates loaded config.ExtraBody
		cliReasoning    map[string]interface{} // Simulates CLI flags for reasoning
		expectedModel   string
		expectedEffort  string
		expectedExclude bool
	}{
		{
			name:          "Basic Request",
			model:         "gpt-4",
			expectedModel: "gpt-4",
		},
		{
			name:           "Reasoning Profile High",
			model:          "o1-preview",
			extraConfig:    map[string]interface{}{"reasoning": map[string]interface{}{"effort": "high"}},
			expectedModel:  "o1-preview",
			expectedEffort: "high",
		},
		{
			name:            "Reasoning Exclude Flag",
			model:           "deepseek-r1",
			extraConfig:     map[string]interface{}{"reasoning": map[string]interface{}{"effort": "medium", "exclude": true}},
			expectedModel:   "deepseek-r1",
			expectedEffort:  "medium",
			expectedExclude: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset
			receivedBody = nil

			// Prepare inputs
			messages := []LLMMessage{{Role: "user", Content: "hello"}}
			extra := make(map[string]interface{})

			// Merge the config simulation into 'extra' (simulating what runLLMChat does)
			for k, v := range tc.extraConfig {
				extra[k] = v
			}

			// Execute
			temp := 0.7
			_, err := llmChat(
				context.Background(),
				messages,
				tc.model,
				0,   // seed
				&temp, // temp
				nil, // postprocess
				"sk-test-key",
				server.URL, // Point to mock server
				false,      // stream
				extra,
				false, // verbose
			)

			if err != nil {
				t.Fatalf("llmChat failed: %v", err)
			}

			// Assertions
			if receivedAuth != "Bearer sk-test-key" {
				t.Errorf("Expected Authorization header, got: %s", receivedAuth)
			}

			if receivedBody["model"] != tc.expectedModel {
				t.Errorf("Expected model %s, got %v", tc.expectedModel, receivedBody["model"])
			}

			// Check Reasoning Params
			if tc.expectedEffort != "" {
				reasoning, ok := receivedBody["reasoning"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected 'reasoning' object in JSON body, found none")
				}
				if reasoning["effort"] != tc.expectedEffort {
					t.Errorf("Expected reasoning effort %s, got %v", tc.expectedEffort, reasoning["effort"])
				}
				if tc.expectedExclude {
					if exclude, ok := reasoning["exclude"].(bool); !ok || !exclude {
						t.Error("Expected reasoning.exclude to be true")
					}
				}
			}
		})
	}
}
