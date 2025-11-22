package main

import (
	"testing"
)

func TestConfigDeepMerge(t *testing.T) {
	// Scenario: User defines a base OpenAI config, extends it for 'coding',
	// then extends 'coding' for a specific model, adding specific parameters at each step.

	codingTemp := 0.2

	cfg := &ConfigFile{
		Models: map[string]ModelConfig{
			"base": {
				ExtraBody: map[string]interface{}{
					"stream_options": map[string]interface{}{
						"include_usage": true,
					},
					"stop": []string{"User:"},
				},
			},
			"coding": {
				Extend:      strPtr("base"),
				Temperature: &codingTemp,
				ExtraBody: map[string]interface{}{
					"response_format": map[string]interface{}{
						"type": "json_object",
					},
				},
			},
			"specific-model": {
				Extend: strPtr("coding"),
				ExtraBody: map[string]interface{}{
					"stop": []string{"End"}, // Should overwrite base stop? or merge? Implementation says overwrite for keys.
				},
			},
		},
	}

	resolved, err := resolveModelConfig(cfg, "specific-model")
	if err != nil {
		t.Fatalf("Failed to resolve config: %v", err)
	}

	// Check inherited value (nested map)
	streamOpts, ok := resolved.ExtraBody["stream_options"].(map[string]interface{})
	if !ok || streamOpts["include_usage"] != true {
		t.Error("Deep merge failed: stream_options lost or incorrect")
	}

	// Check middle layer value
	respFmt, ok := resolved.ExtraBody["response_format"].(map[string]interface{})
	if !ok || respFmt["type"] != "json_object" {
		t.Error("Deep merge failed: response_format lost")
	}

	// Check override behavior
	stopSeq := resolved.ExtraBody["stop"].([]string)
	if len(stopSeq) != 1 || stopSeq[0] != "End" {
		t.Errorf("Deep merge failed: expected stop=['End'], got %v", stopSeq)
	}

	// Check typed field inheritance
	if *resolved.Temperature != 0.2 {
		t.Errorf("Typed field inheritance failed: expected 0.2, got %v", *resolved.Temperature)
	}
}

func strPtr(s string) *string { return &s }
