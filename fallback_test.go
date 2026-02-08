package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestCheckFallbackEligibility(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		settings *FallbackSettings
		want     bool
	}{
		{
			name:     "Default Settings - Allow 500",
			err:      fmt.Errorf("API error (status 500): Internal Error"),
			settings: nil, // Use built-in defaults
			want:     true,
		},
		{
			name:     "Default Settings - Allow 427",
			err:      fmt.Errorf("API error (status 427): Token limit"),
			settings: nil,
			want:     true,
		},
		{
			name:     "Default Settings - Deny 400",
			err:      fmt.Errorf("API error (status 400): Bad Request"),
			settings: nil,
			want:     false,
		},
		{
			name:     "Default Settings - Deny 401",
			err:      fmt.Errorf("API error (status 401): Unauthorized"),
			settings: nil,
			want:     false,
		},
		{
			name:     "Custom Allow Glob",
			err:      fmt.Errorf("API error (status 503): Unavailable"),
			settings: &FallbackSettings{
				Allow: []string{"5*"},
				Default: "deny",
			},
			want:     true,
		},
		{
			name:     "Custom Deny Specific",
			err:      fmt.Errorf("API error (status 418): Teapot"),
			settings: &FallbackSettings{
				Deny: []string{"418"},
				Default: "allow",
			},
			want:     false,
		},
		{
			name:     "Network Error (No Status) - Default Allow",
			err:      errors.New("dial tcp: i/o timeout"),
			settings: &FallbackSettings{Default: "allow"},
			want:     true,
		},
		{
			name:     "Network Error (No Status) - Default Deny",
			err:      errors.New("dial tcp: i/o timeout"),
			settings: &FallbackSettings{Default: "deny"},
			want:     false,
		},
		{
			name:     "Precedence Deny over Allow",
			err:      fmt.Errorf("API error (status 500): Error"),
			settings: &FallbackSettings{
				Deny: []string{"500"},
				Allow: []string{"5*"}, // More general allow
				Default: "deny",
			},
			want:     false, // Deny is checked first in implementation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFallbackEligibility(tt.err, tt.settings)
			if got != tt.want {
				t.Errorf("checkFallbackEligibility() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGlobalFallbackResolution(t *testing.T) {
	// Simulate the logic inside makeLLMApiFunc regarding config merging
	cfg := &ConfigFile{
		FallbackModels: []string{"global-backup"},
	}
	
	runCfgWithSpecific := RunConfig{
		ModelName: "primary",
		Fallback: []string{"specific-backup"},
	}

	runCfgWithoutSpecific := RunConfig{
		ModelName: "primary",
		Fallback: []string{},
	}

	// Case 1: Specific fallback overrides global
	models1 := []string{runCfgWithSpecific.ModelName}
	if len(runCfgWithSpecific.Fallback) > 0 {
		models1 = append(models1, runCfgWithSpecific.Fallback...)
	} else if len(cfg.FallbackModels) > 0 {
		models1 = append(models1, cfg.FallbackModels...)
	}

	if len(models1) != 2 || models1[1] != "specific-backup" {
		t.Errorf("Expected specific backup, got %v", models1)
	}

	// Case 2: Global fallback used when specific is empty
	models2 := []string{runCfgWithoutSpecific.ModelName}
	if len(runCfgWithoutSpecific.Fallback) > 0 {
		models2 = append(models2, runCfgWithoutSpecific.Fallback...)
	} else if len(cfg.FallbackModels) > 0 {
		models2 = append(models2, cfg.FallbackModels...)
	}

	if len(models2) != 2 || models2[1] != "global-backup" {
		t.Errorf("Expected global backup, got %v", models2)
	}
}
