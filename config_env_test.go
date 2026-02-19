package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestConfigEnvExpansionAndPrecedence verifies:
// 1. Environment variables in config.yaml are expanded (e.g. $VAR).
// 2. Configuration values take precedence over global environment variables (e.g. OPENAI_API_KEY).
// 3. CLI flags take precedence over Configuration values.
func TestConfigEnvExpansionAndPrecedence(t *testing.T) {
	// 1. Setup Temporary Home Directory
	tmpHome, err := os.MkdirTemp("", "llm-test-home-env")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpHome)

	// Mock HOME to point to our temp dir so loadConfig finds our config.yaml
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", oldHome)

	configDir := filepath.Join(tmpHome, ".llmterm")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	// 2. Setup Test Environment Variables
	testKey := "sk-test-secret-key-123"
	testBase := "https://api.test.com/v1"
	
	os.Setenv("TEST_EXPAND_KEY", testKey)
	defer os.Unsetenv("TEST_EXPAND_KEY")

	os.Setenv("TEST_EXPAND_BASE", testBase)
	defer os.Unsetenv("TEST_EXPAND_BASE")

	// 3. Create Config File with Env Vars
	configContent := `
models:
  env-model:
    api_key: $TEST_EXPAND_KEY
    api_base: ${TEST_EXPAND_BASE}
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// 4. Test: Expansion Logic (loadConfig)
	t.Run("Expansion", func(t *testing.T) {
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig failed: %v", err)
		}

		modelCfg, ok := cfg.Models["env-model"]
		if !ok {
			t.Fatal("env-model not found in config")
		}

		if modelCfg.ApiKey == nil || *modelCfg.ApiKey != testKey {
			t.Errorf("ApiKey expansion failed. Want %q, got %v", testKey, modelCfg.ApiKey)
		}

		if modelCfg.ApiBase == nil || *modelCfg.ApiBase != testBase {
			t.Errorf("ApiBase expansion failed. Want %q, got %v", testBase, modelCfg.ApiBase)
		}
	})

	// 5. Test: Precedence (getRunConfig)
	// Scenario: Config (Specific) vs Global Env (Generic)
	// Expectation: Config wins
	t.Run("Precedence_ConfigVsGlobalEnv", func(t *testing.T) {
		// Set global env vars that usually drive defaults
		os.Setenv("OPENAI_API_KEY", "global-ignored-key")
		defer os.Unsetenv("OPENAI_API_KEY")
		
		os.Setenv("OPENAI_API_BASE", "https://global.ignored.com")
		defer os.Unsetenv("OPENAI_API_BASE")

		// Load clean config
		cfg, _ := loadConfig()
		
		// Create dummy command with no flags set
		cmd := &cobra.Command{}
		registerTestFlags(cmd)

		runCfg, err := getRunConfig(cmd, cfg, "env-model")
		if err != nil {
			t.Fatalf("getRunConfig failed: %v", err)
		}

		if runCfg.ApiKey != testKey {
			t.Errorf("Precedence Error: Config ApiKey should override OPENAI_API_KEY. Want %q, got %q", testKey, runCfg.ApiKey)
		}
		if runCfg.ApiBase != testBase {
			t.Errorf("Precedence Error: Config ApiBase should override OPENAI_API_BASE. Want %q, got %q", testBase, runCfg.ApiBase)
		}
	})

	// 6. Test: Precedence (Flag vs Config)
	// Scenario: Flag (Explicit) vs Config (Specific)
	// Expectation: Flag wins
	t.Run("Precedence_FlagVsConfig", func(t *testing.T) {
		cfg, _ := loadConfig()
		cmd := &cobra.Command{}
		registerTestFlags(cmd)

		// Set flags
		flagKey := "sk-flag-key"
		flagBase := "https://flag.com"
		cmd.Flags().Set("api-key", flagKey)
		cmd.Flags().Set("api-base", flagBase)

		runCfg, err := getRunConfig(cmd, cfg, "env-model")
		if err != nil {
			t.Fatalf("getRunConfig failed: %v", err)
		}

		if runCfg.ApiKey != flagKey {
			t.Errorf("Precedence Error: Flag ApiKey should override Config. Want %q, got %q", flagKey, runCfg.ApiKey)
		}
		if runCfg.ApiBase != flagBase {
			t.Errorf("Precedence Error: Flag ApiBase should override Config. Want %q, got %q", flagBase, runCfg.ApiBase)
		}
	})
}

// Helper to register flags needed by getRunConfig
func registerTestFlags(cmd *cobra.Command) {
	cmd.Flags().String("api-key", "", "")
	cmd.Flags().String("api-base", "", "")
	cmd.Flags().Float64("temperature", 0.0, "")
	cmd.Flags().Int("timeout", 0, "")
	cmd.Flags().Int("seed", 0, "")
	cmd.Flags().Int("max_tokens", 0, "")
	cmd.Flags().String("reasoning", "", "")
	cmd.Flags().Bool("no-reasoning", false, "")
	cmd.Flags().Bool("reasoning-low", false, "")
	cmd.Flags().Bool("reasoning-medium", false, "")
	cmd.Flags().Bool("reasoning-high", false, "")
	cmd.Flags().Bool("reasoning-xhigh", false, "")
	cmd.Flags().Int("reasoning-max", 0, "")
	cmd.Flags().Bool("reasoning-exclude", false, "")
	cmd.Flags().String("verbosity", "", "")
	cmd.Flags().String("context-order", "", "")
}
