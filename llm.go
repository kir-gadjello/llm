package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kir-gadjello/llm/history"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	markdown "github.com/vlanse/go-term-markdown"

	"gopkg.in/yaml.v3"
)

var TEXTINPUT_PLACEHOLDER = "Type a message and press Enter to send..."

var startTime = time.Now()
var historyMgr *history.Manager

func is_interactive(fd uintptr) bool {
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

type Timings struct {
	BinaryStartup      time.Duration
	TimeToFirstLLMCall time.Duration
	TimeToFirstChunk   time.Duration
	TimeToComplete     time.Duration
}

func displayTimings(t Timings) {
	fmt.Fprintf(os.Stderr, "\n=== TIMING INFORMATION ===\n")
	fmt.Fprintf(os.Stderr, "Binary Startup:        %v\n", t.BinaryStartup)
	fmt.Fprintf(os.Stderr, "Time to First LLM Call: %v\n", t.TimeToFirstLLMCall)
	fmt.Fprintf(os.Stderr, "Time to First Response: %v\n", t.TimeToFirstChunk)
	fmt.Fprintf(os.Stderr, "Time to Complete:       %v\n", t.TimeToComplete)
	fmt.Fprintf(os.Stderr, "==========================\n")
}

type Message struct {
	UUID    string   `json:"uuid"`
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

func NewMessage(role, content string) *Message {
	uuid := generateUUID()

	return &Message{
		UUID:    uuid,
		Role:    role,
		Content: content,
	}
}

func formatContext(files []FileContext, format string, filenameMode string, cwd string, truncateLimit int, useOuterTags bool) (string, []string) {
	var buf strings.Builder
	var images []string

	for _, f := range files {
		displayPath := f.Path
		switch filenameMode {
		case "relative":
			if rel, err := filepath.Rel(cwd, f.Path); err == nil {
				displayPath = rel
			}
		case "name-only":
			displayPath = filepath.Base(f.Path)
		case "none":
			displayPath = ""
		case "absolute":
			// keep absolute
		}

		if format == "md" {
			if displayPath != "" {
				buf.WriteString(fmt.Sprintf("File: %s\n", displayPath))
			}
			if f.IsImage {
				buf.WriteString(fmt.Sprintf("[Image: %s]\n", displayPath))
				images = append(images, f.Content)
			} else if f.IsBinary {
				buf.WriteString("[Binary File]\n")
			} else {
				content := f.Content
				if truncateLimit > 0 {
					lines := strings.Split(content, "\n")
					if len(lines) > truncateLimit {
						content = strings.Join(lines[:truncateLimit], "\n") + fmt.Sprintf("\n... [truncated %d lines] ...", len(lines)-truncateLimit)
					}
				}
				buf.WriteString(fmt.Sprintf("```%s\n", f.Type))
				buf.WriteString(content)
				if !strings.HasSuffix(content, "\n") {
					buf.WriteString("\n")
				}
				buf.WriteString("```\n")
			}
			buf.WriteString("\n")
		} else {
			// xml
			if displayPath != "" {
				buf.WriteString(fmt.Sprintf("<file path=\"%s\">\n", displayPath))
			} else {
				buf.WriteString("<file>\n")
			}
			if f.IsImage {
				buf.WriteString(fmt.Sprintf("[Image: %s]\n", displayPath))
				images = append(images, f.Content)
			} else if f.IsBinary {
				buf.WriteString("[Binary File]\n")
			} else {
				content := f.Content
				if truncateLimit > 0 {
					lines := strings.Split(content, "\n")
					if len(lines) > truncateLimit {
						content = strings.Join(lines[:truncateLimit], "\n") + fmt.Sprintf("\n... [truncated %d lines] ...", len(lines)-truncateLimit)
					}
				}
				buf.WriteString(content)
			}
			buf.WriteString("\n</file>\n")
		}
	}

	if format != "md" {
		inner := buf.String()
		// If using outer tags (armor disabled or implicit), wrap in context
		// Otherwise just return the files block to be wrapped by global armor
		if useOuterTags {
			return "<context>\n<files>\n" + inner + "</files>\n</context>", images
		}
		return "<files>\n" + inner + "</files>\n", images
	}

	return buf.String(), images
}

type ModelConfig struct {
	Model              *string                `yaml:"model,omitempty"`
	ApiBase            *string                `yaml:"api_base,omitempty"`
	ApiKey             *string                `yaml:"api_key,omitempty"`
	Temperature        *float64               `yaml:"temperature,omitempty"`
	Timeout            *int                   `yaml:"timeout,omitempty"` // Seconds
	Seed               *int                   `yaml:"seed,omitempty"`
	MaxTokens          *int                   `yaml:"max_tokens,omitempty"`
	ReasoningEffort    *string                `yaml:"reasoning_effort,omitempty"`
	ReasoningMaxTokens *int                   `yaml:"reasoning_max_tokens,omitempty"`
	ReasoningExclude   *bool                  `yaml:"reasoning_exclude,omitempty"`
	Verbosity          *string                `yaml:"verbosity,omitempty"`
	ContextOrder       *string                `yaml:"context_order,omitempty"`
	ExtraBody          map[string]interface{} `yaml:"extra_body,omitempty"`
	Extend             *string                `yaml:"extend,omitempty"`
	Aliases            []string               `yaml:"aliases,omitempty"`
}

type ShellConfig struct {
	Yolo *bool `yaml:"yolo,omitempty"`
}

type ContextConfig struct {
	AutoSelectorModel  *string  `yaml:"auto_selector_model,omitempty"`
	MaxFileSizeKB      *int     `yaml:"max_file_size_kb,omitempty"`
	// Optional separate limit for images (default 10MB)
	MaxImageSizeKB     *int     `yaml:"max_image_size_kb,omitempty"`
	MaxRepoFiles       *int     `yaml:"max_repo_files,omitempty"`
	IgnoredDirs        []string `yaml:"ignored_dirs,omitempty"`
	DebugTruncateFiles *int     `yaml:"debug_truncate_files,omitempty"`
}

type ConfigFile struct {
	Default             string                 `yaml:"default,omitempty"`
	Timeout             *int                   `yaml:"timeout,omitempty"` // Global default in seconds
	ContextArmor        *bool                  `yaml:"context_armor,omitempty"`
	PipedInputWrapper   *string                `yaml:"piped_input_wrapper,omitempty"`
	LogReasoning        *bool                  `yaml:"log_reasoning,omitempty"`
	LogReasoningShorten *int                   `yaml:"log_reasoning_shorten,omitempty"`
	ThinkingStartTag    *string                `yaml:"thinking_start_tag,omitempty"`
	ThinkingEndTag      *string                `yaml:"thinking_end_tag,omitempty"`
	Models              map[string]ModelConfig `yaml:"models,omitempty"`
	Shell               *ShellConfig           `yaml:"shell,omitempty"`
	Context             *ContextConfig         `yaml:"context,omitempty"`
}

func loadConfig() (*ConfigFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		// Don't fail completely if we can't get home dir
		return &ConfigFile{}, nil
	}

	configDir := filepath.Join(home, ".llmterm")
	configPath := filepath.Join(configDir, "config.yaml")
	oldConfigPath := filepath.Join(home, ".llmterm.yaml")

	// Try new location first
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Try old location
			data, err = os.ReadFile(oldConfigPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					// No config file exists, create directory structure and return empty config
					if err := os.MkdirAll(configDir, 0o755); err != nil {
						// Don't fail if we can't create directory
						return &ConfigFile{}, nil
					}
					// Also create cache directory
					cacheDir := filepath.Join(configDir, "cache")
					os.MkdirAll(cacheDir, 0o755) // Ignore error
					return &ConfigFile{}, nil
				}
				// Can't read existing config, but don't fail the program
				return &ConfigFile{}, nil
			}
			// Found old config, use it but warn user
			fmt.Fprintf(os.Stderr, "Note: Using config from %s. Consider moving it to %s\n",
				oldConfigPath, configPath)
		} else {
			// Can't read config, but don't fail the program
			return &ConfigFile{}, nil
		}
	}

	var cfg ConfigFile
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Expand aliases
	if cfg.Models != nil {
		aliasMap := make(map[string]ModelConfig)
		for name, config := range cfg.Models {
			for _, alias := range config.Aliases {
				// Check against main keys
				if _, exists := cfg.Models[alias]; exists {
					fmt.Fprintf(os.Stderr, "Warning: alias '%s' defined in model '%s' clashes with existing model. Ignoring alias.\n", alias, name)
					continue
				}
				// Check against other aliases found so far
				if _, exists := aliasMap[alias]; exists {
					fmt.Fprintf(os.Stderr, "Warning: duplicate alias '%s' defined in model '%s'. Ignoring.\n", alias, name)
					continue
				}

				// Create an alias entry that extends the original model
				parentName := name
				aliasMap[alias] = ModelConfig{
					Extend: &parentName,
				}
			}
		}
		// Merge aliases
		for k, v := range aliasMap {
			cfg.Models[k] = v
		}
	}

	// Ensure directory structure exists (but don't fail if we can't create)
	os.MkdirAll(configDir, 0o755)
	cacheDir := filepath.Join(configDir, "cache")
	os.MkdirAll(cacheDir, 0o755)

	return &cfg, nil
}

func mergeMaps(base, override map[string]interface{}) map[string]interface{} {
	if base == nil {
		base = make(map[string]interface{})
	}
	if override == nil {
		return base
	}

	result := make(map[string]interface{})
	for k, v := range base {
		result[k] = v
	}

	for k, v := range override {
		if baseVal, ok := result[k]; ok {
			// If both are maps, recurse
			baseMap, baseOk := baseVal.(map[string]interface{})
			overrideMap, overrideOk := v.(map[string]interface{})
			if baseOk && overrideOk {
				result[k] = mergeMaps(baseMap, overrideMap)
				continue
			}
		}
		// Otherwise overwrite
		result[k] = v
	}
	return result
}

func resolveModelConfig(cfg *ConfigFile, modelName string) (ModelConfig, error) {
	// Handle empty config
	if cfg == nil || cfg.Models == nil || len(cfg.Models) == 0 {
		return ModelConfig{}, nil
	}

	// Early return for empty model name
	if modelName == "" {
		return ModelConfig{}, nil
	}

	return resolveModelConfigRec(cfg, modelName, map[string]bool{})
}

func resolveModelConfigRec(cfg *ConfigFile, modelName string, visited map[string]bool) (ModelConfig, error) {
	// Early return for empty model name
	if modelName == "" {
		return ModelConfig{}, nil
	}

	if visited[modelName] {
		return ModelConfig{}, fmt.Errorf("circular dependency detected for model: %s", modelName)
	}
	visited[modelName] = true

	modelCfg, ok := cfg.Models[modelName]
	if !ok {
		// If the model doesn't exist in config, we return an empty config
		// This allows using models that are not explicitly defined but might be defaults
		return ModelConfig{}, nil
	}

	if modelCfg.Extend != nil {
		parentName := *modelCfg.Extend
		parentCfg, err := resolveModelConfigRec(cfg, parentName, visited)
		if err != nil {
			return ModelConfig{}, err
		}

		// Merge parent into child (child overrides parent)
		merged := parentCfg // Start with parent

		if modelCfg.Model != nil {
			merged.Model = modelCfg.Model
		}
		if modelCfg.ApiBase != nil {
			merged.ApiBase = modelCfg.ApiBase
		}
		if modelCfg.ApiKey != nil {
			merged.ApiKey = modelCfg.ApiKey
		}
		if modelCfg.Temperature != nil {
			merged.Temperature = modelCfg.Temperature
		}
		if modelCfg.Timeout != nil {
			merged.Timeout = modelCfg.Timeout
		}
		if modelCfg.Seed != nil {
			merged.Seed = modelCfg.Seed
		}
		if modelCfg.MaxTokens != nil {
			merged.MaxTokens = modelCfg.MaxTokens
		}
		if modelCfg.ReasoningEffort != nil {
			merged.ReasoningEffort = modelCfg.ReasoningEffort
		}
		if modelCfg.ReasoningMaxTokens != nil {
			merged.ReasoningMaxTokens = modelCfg.ReasoningMaxTokens
		}
		if modelCfg.ReasoningExclude != nil {
			merged.ReasoningExclude = modelCfg.ReasoningExclude
		}
		if modelCfg.Verbosity != nil {
			merged.Verbosity = modelCfg.Verbosity
		}
		if modelCfg.ContextOrder != nil {
			merged.ContextOrder = modelCfg.ContextOrder
		}

		merged.ExtraBody = mergeMaps(merged.ExtraBody, modelCfg.ExtraBody)

		// Extend is handled by the recursion, so we don't need to copy it,
		// but for correctness of the struct state, we can leave it or clear it.
		// Let's keep the child's extend value.
		merged.Extend = modelCfg.Extend

		return merged, nil
	}

	return modelCfg, nil
}

type RunConfig struct {
	ModelName          string
	ApiKey             string
	ApiBase            string
	Temperature        *float64
	Timeout            time.Duration
	Seed               int
	MaxTokens          int
	ReasoningEffort    string
	ReasoningMaxTokens int
	ReasoningExclude   bool
	Verbosity          string
	ContextOrder       string
	ExtraBody          map[string]interface{}
}

func getRunConfig(cmd *cobra.Command, cfg *ConfigFile, modelname string) (RunConfig, error) {
	// 1. Initial values from flags (defaults or user-provided)
	apiKey, _ := cmd.Flags().GetString("api-key")
	apiBase, _ := cmd.Flags().GetString("api-base")

	var temperature *float64
	if cmd.Flags().Changed("temperature") {
		t, _ := cmd.Flags().GetFloat64("temperature")
		temperature = &t
	}

	timeoutSec, _ := cmd.Flags().GetInt("timeout")
	seed, _ := cmd.Flags().GetInt("seed")
	maxTokens, _ := cmd.Flags().GetInt("max_tokens")
	contextOrder, _ := cmd.Flags().GetString("context-order")

	// Reasoning flags
	reasoningArg, _ := cmd.Flags().GetString("reasoning")
	noReasoning, _ := cmd.Flags().GetBool("no-reasoning")
	reasoningLow, _ := cmd.Flags().GetBool("reasoning-low")
	reasoningMedium, _ := cmd.Flags().GetBool("reasoning-medium")
	reasoningHigh, _ := cmd.Flags().GetBool("reasoning-high")
	reasoningXHigh, _ := cmd.Flags().GetBool("reasoning-xhigh")
	reasoningMax, _ := cmd.Flags().GetInt("reasoning-max")
	reasoningExclude, _ := cmd.Flags().GetBool("reasoning-exclude")

	// Verbosity
	verbosity, _ := cmd.Flags().GetString("verbosity")

	reasoningEffort := ""

	if reasoningArg != "" {
		reasoningEffort = reasoningArg
	} else if noReasoning {
		reasoningEffort = "none"
	} else if reasoningLow {
		reasoningEffort = "low"
	} else if reasoningMedium {
		reasoningEffort = "medium"
	} else if reasoningHigh {
		reasoningEffort = "high"
	} else if reasoningXHigh {
		reasoningEffort = "xhigh"
	}

	extraBody := make(map[string]interface{})

	// 2. Resolve Model Config from file
	// Default timeout: 3000 seconds (50 minutes) if not specified anywhere
	finalTimeout := 3000

	if cfg.Timeout != nil {
		finalTimeout = *cfg.Timeout
	}

	if len(modelname) > 0 {
		resolvedCfg, err := resolveModelConfig(cfg, modelname)
		if err != nil {
			// Log warning but continue with flags
			// log.Printf("Warning: failed to resolve config for model %s: %v", modelname, err)
		} else {
			if resolvedCfg.Model != nil {
				modelname = *resolvedCfg.Model
			}
			if resolvedCfg.ApiKey != nil && !cmd.Flags().Changed("api-key") && os.Getenv("OPENAI_API_KEY") == "" {
				apiKey = *resolvedCfg.ApiKey
			}
			if resolvedCfg.ApiBase != nil && !cmd.Flags().Changed("api-base") && os.Getenv("OPENAI_API_BASE") == "" {
				apiBase = *resolvedCfg.ApiBase
			}
			if resolvedCfg.Temperature != nil && !cmd.Flags().Changed("temperature") {
				temperature = resolvedCfg.Temperature
			}
			if resolvedCfg.Timeout != nil {
				finalTimeout = *resolvedCfg.Timeout
			}
			if resolvedCfg.Seed != nil && !cmd.Flags().Changed("seed") {
				seed = *resolvedCfg.Seed
			}
			if resolvedCfg.MaxTokens != nil && !cmd.Flags().Changed("max_tokens") {
				maxTokens = *resolvedCfg.MaxTokens
			}

			// Apply reasoning config if not specified via flags
			reasoningFlagsChanged := cmd.Flags().Changed("reasoning") ||
				cmd.Flags().Changed("no-reasoning") ||
				cmd.Flags().Changed("reasoning-low") ||
				cmd.Flags().Changed("reasoning-medium") ||
				cmd.Flags().Changed("reasoning-high") ||
				cmd.Flags().Changed("reasoning-xhigh")

			if resolvedCfg.ReasoningEffort != nil && !reasoningFlagsChanged {
				reasoningEffort = *resolvedCfg.ReasoningEffort
			}
			if resolvedCfg.ReasoningMaxTokens != nil && !cmd.Flags().Changed("reasoning-max") {
				reasoningMax = *resolvedCfg.ReasoningMaxTokens
			}
			if resolvedCfg.ReasoningExclude != nil && !cmd.Flags().Changed("reasoning-exclude") {
				reasoningExclude = *resolvedCfg.ReasoningExclude
			}
			if resolvedCfg.Verbosity != nil && !cmd.Flags().Changed("verbosity") {
				verbosity = *resolvedCfg.Verbosity
			}
			if resolvedCfg.ContextOrder != nil && !cmd.Flags().Changed("context-order") {
				contextOrder = *resolvedCfg.ContextOrder
			}

			// Merge ExtraBody
			if resolvedCfg.ExtraBody != nil {
				extraBody = mergeMaps(extraBody, resolvedCfg.ExtraBody)
			}
		}
	}

	// CLI flag overrides config
	if cmd.Flags().Changed("timeout") {
		finalTimeout = timeoutSec
	}

	return RunConfig{
		ModelName:          modelname,
		ApiKey:             apiKey,
		ApiBase:            apiBase,
		Temperature:        temperature,
		Timeout:            time.Duration(finalTimeout) * time.Second,
		Seed:               seed,
		MaxTokens:          maxTokens,
		ReasoningEffort:    reasoningEffort,
		ReasoningMaxTokens: reasoningMax,
		ReasoningExclude:   reasoningExclude,
		Verbosity:          verbosity,
		ContextOrder:       contextOrder,
		ExtraBody:          extraBody,
	}, nil
}

func putTextIntoClipboard(text string) error {
	return clipboard.WriteAll(text)
}

type Session struct {
	UUID      string
	Timestamp time.Time
}

func newSession() *Session {
	uuid := generateUUID()
	return &Session{UUID: uuid, Timestamp: time.Now()}
}

func generateUUID() string {
	u := make([]byte, 16)
	_, err := rand.Read(u)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixMilli())
	}
	return base64.URLEncoding.EncodeToString(u)
}

func sanitizeFilename(name string) string {
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|", " "}
	for _, char := range invalid {
		name = strings.ReplaceAll(name, char, "_")
	}
	return name
}

func saveOutput(pathStr string, content string, modelName string) error {
	cwd, _ := os.Getwd()
	if pathStr == "." {
		pathStr = cwd
	}

	// Check if pathStr looks like a directory or existing directory
	info, err := os.Stat(pathStr)
	isDir := (err == nil && info.IsDir()) || strings.HasSuffix(pathStr, string(os.PathSeparator)) || strings.HasSuffix(pathStr, "/")

	var finalPath string

	if isDir {
		// Generate filename
		timestamp := time.Now().Format("2006-01-02_15-04-05")
		filename := fmt.Sprintf("%s_%s.md", sanitizeFilename(modelName), timestamp)
		finalPath = filepath.Join(pathStr, filename)

		// Ensure directory exists
		if err := os.MkdirAll(pathStr, 0755); err != nil {
			return err
		}
	} else {
		finalPath = pathStr
		// Ensure parent directory exists
		dir := filepath.Dir(finalPath)
		if dir != "." && dir != "/" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}

		// Ensure extension
		if filepath.Ext(finalPath) == "" {
			finalPath += ".md"
		}
	}

	// Handle collision
	base := strings.TrimSuffix(finalPath, filepath.Ext(finalPath))
	ext := filepath.Ext(finalPath)

	for i := 0; ; i++ {
		target := finalPath
		if i > 0 {
			target = fmt.Sprintf("%s_%d%s", base, i, ext)
		}

		if _, err := os.Stat(target); os.IsNotExist(err) {
			finalPath = target
			break
		}
	}

	if err := os.WriteFile(finalPath, []byte(content), 0644); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\nSaved output to %s\n", finalPath)
	return nil
}

// Legacy dumpToHistory replaced by global historyMgr
func dumpToHistory(session *Session, data interface{}) error {
	return nil
}

func main() {

	// Define Root Command
	rootCmd := &cobra.Command{
		Use:   "llm-chat",
		Short: "LLM Chat CLI tool",
		// Explicitly allow arbitrary arguments to avoid "unknown command" errors when subcommands exist
		Args: cobra.ArbitraryArgs,
		// RunE handles the default behavior (llm "query")
		RunE:             runLLMChat,
		TraverseChildren: true,
	}

	var is_terminal bool = is_interactive(os.Stdout.Fd())

	rootCmd.Flags().StringP("model", "m", "", "LLM model: OPENAI_API_MODEL,GROQ_API_MODEL,LLM_MODEL from env or gpt-3.5-turbo")
	rootCmd.Flags().StringP("prompt", "p", "", "System prompt")
	rootCmd.Flags().Float64P("temperature", "t", 0.0, "Temperature")
	rootCmd.Flags().Int("timeout", 4200, "API timeout in seconds (default 70 mins)")
	rootCmd.Flags().IntP("seed", "r", 1337, "Random seed")
	rootCmd.Flags().IntP("max_tokens", "N", 0, "Max amount of tokens in response (0 = model default)")
	rootCmd.Flags().BoolP("stream", "S", is_terminal, "Stream output")

	// Reasoning controls
	rootCmd.Flags().String("reasoning", "", "Reasoning effort (low, medium, high, etc.)")
	rootCmd.Flags().BoolP("no-reasoning", "n", false, "Disable reasoning entirely")
	rootCmd.Flags().Bool("reasoning-low", false, "Reasoning effort: low/minimal (~10-20% tokens)")
	rootCmd.Flags().Bool("reasoning-medium", false, "Reasoning effort: medium (~50%)")
	rootCmd.Flags().Bool("reasoning-high", false, "Reasoning effort: high (~80%)")
	rootCmd.Flags().Bool("reasoning-xhigh", false, "Reasoning effort: xhigh (max reasoning)")
	rootCmd.Flags().IntP("reasoning-max", "R", 0, "Reasoning max_tokens (e.g., -R2048)")
	rootCmd.Flags().Bool("reasoning-exclude", false, "Use reasoning but exclude from response")
	// Aliases
	rootCmd.Flags().SetAnnotation("reasoning-low", cobra.BashCompOneRequiredFlag, []string{"false"})
	rootCmd.Flags().SetAnnotation("reasoning-medium", cobra.BashCompOneRequiredFlag, []string{"false"})
	rootCmd.Flags().SetAnnotation("reasoning-high", cobra.BashCompOneRequiredFlag, []string{"false"})
	rootCmd.Flags().SetAnnotation("reasoning-xhigh", cobra.BashCompOneRequiredFlag, []string{"false"})

	// Verbosity
	rootCmd.Flags().String("verbosity", "", "Response verbosity: low|medium|high")

	// Chat/IO
	rootCmd.Flags().BoolP("chat", "c", false, "Launch chat mode")
	rootCmd.Flags().BoolP("chat-send", "C", false, "Launch chat mode and send the first message right away")
	rootCmd.Flags().BoolP("clipboard", "x", false, "Paste clipboard content as <user-clipboard-content>")
	rootCmd.Flags().String("context-order", "append", "Context ordering for clipboard: prepend|append")
	rootCmd.Flags().StringP("piped-wrapper", "w", "context", "Wrapper tag for piped stdin (empty string disables wrapping)")
	rootCmd.Flags().StringSliceP("files", "f", []string{}, "List of files and directories to include in context (supports globs and comma-separated lists)")
	rootCmd.Flags().Bool("no-gitignore", false, "Do not ignore files matched by .gitignore")
	rootCmd.Flags().Bool("no-ignored-files", false, "Do not ignore default large/unsuitable files (e.g. package-lock.json)")
	rootCmd.Flags().StringP("context-format", "i", "md", "Context (files) input template format (md|xml)")
	rootCmd.Flags().String("show-filenames", "relative", "How to show filenames (absolute|relative|name-only|none)")
	rootCmd.Flags().Bool("context-armor", true, "Wrap context in <context> tags")
	rootCmd.Flags().BoolP("auto", "A", false, "Auto-select files using LLM")
	rootCmd.Flags().String("save-to", "", "Save output to file (dir or path). If flag is present but no value, defaults to CWD")
	rootCmd.Flags().Lookup("save-to").NoOptDefVal = "."
	// Image preview flags
	rootCmd.Flags().Bool("image-log", true, "Preview images sent to model in terminal (if supported)")
	rootCmd.Flags().Bool("no-image-log", false, "Disable image preview in terminal")

	// API/Debug
	rootCmd.Flags().StringP("api-key", "k", "", "OpenAI API key")
	rootCmd.Flags().StringP("api-base", "b", "https://api.openai.com/v1/", "OpenAI API base URL")
	rootCmd.Flags().StringP("extra", "e", "{}", "Additional LLM API parameters expressed as json, take precedence over provided CLI arguments")
	rootCmd.Flags().BoolP("json", "j", false, "json mode")
	rootCmd.Flags().StringP("json-schema", "J", "", "json schema (compatible with llama.cpp and tabbyAPI, not compatible with OpenAI)")
	rootCmd.Flags().StringP("stop", "X", "", "Stop sequences (a single word or a json array)")
	rootCmd.Flags().BoolP("debug", "D", false, "Output prompt & system msg")
	rootCmd.Flags().BoolP("verbose", "v", false, "http & debug logging")
	rootCmd.Flags().BoolP("dry", "d", false, "Dry run: print token stats and parameters without making network requests")
	rootCmd.Flags().Bool("vt", false, "Lean timing debug: output response performance metrics (TTFT, TPS, etc.)")

	// Shell Assistant
	rootCmd.Flags().BoolP("shell", "s", false, "Shell Assistant: generate and execute shell commands")
	rootCmd.Flags().BoolP("yolo", "y", false, "Shell Assistant: execute commands without confirmation")
	rootCmd.Flags().IntP("history", "H", 0, "Include shell history (default 20 lines)")
	rootCmd.Flags().Lookup("history").NoOptDefVal = "20"

	// Session Mode Flag
	rootCmd.Flags().Bool("session", false, "Start a transparent shell session with '??' AI interception")

	// NEW: Session Subcommand
	sessionCmd := &cobra.Command{
		Use:   "session",
		Short: "Start a terminal session with AI superpowers",
		Long:  "Starts your default shell wrapped in a harness. Type '?? your question' to invoke the LLM with full context.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				cfg = &ConfigFile{}
			}
			return runSession(cmd, args, cfg)
		},
	}
	// Propagate flags to session command so they are available inside
	sessionCmd.Flags().AddFlagSet(rootCmd.Flags())
	rootCmd.AddCommand(sessionCmd)

	// NEW: Integration Subcommand
	integrationCmd := &cobra.Command{
		Use:   "integration [shell]",
		Short: "Print shell integration scripts (zsh, bash, fish)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printShellIntegration(args[0])
		},
	}
	rootCmd.AddCommand(integrationCmd)

	// NEW: Search Subcommand
	searchCmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search conversation history",
		Long:  "Search for messages in history. Use 'user:term' or 'ai:term' to filter by role.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if historyMgr == nil {
				return fmt.Errorf("history manager not initialized")
			}
			results, err := historyMgr.Search(args[0])
			if err != nil {
				return err
			}
			if len(results) == 0 {
				fmt.Println("No matches found.")
				return nil
			}
			for _, r := range results {
				fmt.Printf("\033[1;34m%s\033[0m [%s] (%s): %s\n", r.Timestamp.Format("2006-01-02 15:04"), r.SessionUUID[:8], r.Role, r.Preview)
			}
			return nil
		},
	}
	rootCmd.AddCommand(searchCmd)

	// NEW: History Command
	historyCmd := &cobra.Command{
		Use:   "history",
		Short: "Browse and resume recent chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			if historyMgr == nil {
				return fmt.Errorf("history manager not initialized")
			}

			sessions, err := historyMgr.ListRecentSessions(50)
			if err != nil {
				return err
			}

			if !is_interactive(os.Stdout.Fd()) {
				for _, s := range sessions {
					fmt.Printf("%s\t%s\t%s\t%s\n", s.UUID, s.Timestamp.Format("2006-01-02 15:04"), s.Model, s.Summary)
				}
				return nil
			}

			m := newHistoryModel(sessions)
			p := tea.NewProgram(m, tea.WithAltScreen())
			finalM, err := p.Run()
			if err != nil {
				return err
			}

			finalHistory := finalM.(historyModel)
			if finalHistory.selected != nil {
				// Resume logic
				uuid := finalHistory.selected.UUID
				msgs, err := historyMgr.GetSessionMessages(uuid)
				if err != nil {
					return fmt.Errorf("failed to load session: %w", err)
				}

				var llmMsgs []Message
				for _, m := range msgs {
					if m.Role == "__sys__" {
						var sysOp map[string]string
						if err := json.Unmarshal([]byte(m.Content), &sysOp); err == nil {
							if sysOp["sysop"] == "remove_msg" {
								targetID := sysOp["id"]
								n := 0
								for _, active := range llmMsgs {
									if active.UUID != targetID {
										llmMsgs[n] = active
										n++
									}
								}
								llmMsgs = llmMsgs[:n]
							}
						}
						continue
					}
					llmMsgs = append(llmMsgs, Message{
						Role:    m.Role,
						Content: m.Content,
						UUID:    m.UUID,
					})
				}

				resumedMessages = llmMsgs
				resumedSessionUUID = uuid
				if !cmd.Flags().Changed("model") {
					cmd.Flags().Set("model", finalHistory.selected.Model)
				}
				cmd.Flags().Set("chat", "true")

				return runLLMChat(cmd, []string{})
			}

			return nil
		},
	}
	historyCmd.Flags().AddFlagSet(rootCmd.Flags())
	rootCmd.AddCommand(historyCmd)

	// NEW: Doctor Subcommand (System Check)
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check system capabilities and dependencies",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("LLM CLI Doctor")
			fmt.Println("==============")

			// 1. Check FTS5 Support
			if history.CheckFTS() {
				fmt.Println("✅ SQLite FTS5   : Enabled (Search Available)")
			} else {
				fmt.Println("❌ SQLite FTS5   : Disabled")
				fmt.Println("   -> FIX: Build with '-tags sqlite_fts5'")
			}

			// 2. Check Config
			home, _ := os.UserHomeDir()
			configPath := filepath.Join(home, ".llmterm", "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				fmt.Printf("✅ Configuration : Found (%s)\n", configPath)
			} else {
				fmt.Printf("⚠️  Configuration : Missing (%s)\n", configPath)
			}

			// 3. Check API Key
			if os.Getenv("OPENAI_API_KEY") != "" {
				fmt.Println("✅ OPENAI_API_KEY: Set")
			} else {
				fmt.Println("⚠️  OPENAI_API_KEY: Not set (Check env or config)")
			}
		},
	}
	rootCmd.AddCommand(doctorCmd)

	// NEW: Resume Subcommand
	resumeCmd := &cobra.Command{
		Use:   "resume [uuid] [message]",
		Short: "Resume a previous session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if historyMgr == nil {
				return fmt.Errorf("history manager not initialized")
			}

			// Resolve UUID
			partial := args[0]
			uuid, err := historyMgr.ResolveSessionUUID(partial)
			if err != nil {
				return err
			}

			msgs, err := historyMgr.GetSessionMessages(uuid)
			if err != nil {
				return fmt.Errorf("failed to load session: %w", err)
			}
			if len(msgs) == 0 {
				return fmt.Errorf("session not found or empty")
			}

			// Reconstruct messages handling removals
			var llmMsgs []Message
			for _, m := range msgs {
				if m.Role == "__sys__" {
					var sysOp map[string]string
					if err := json.Unmarshal([]byte(m.Content), &sysOp); err == nil {
						if sysOp["sysop"] == "remove_msg" {
							targetID := sysOp["id"]
							// Remove from llmMsgs
							n := 0
							for _, active := range llmMsgs {
								if active.UUID != targetID {
									llmMsgs[n] = active
									n++
								}
							}
							llmMsgs = llmMsgs[:n]
						}
					}
					continue
				}

				llmMsgs = append(llmMsgs, Message{
					Role:    m.Role,
					Content: m.Content,
					UUID:    m.UUID,
				})
			}

			// Handle input arguments
			var nextPrompt []string
			if len(args) > 1 {
				// Non-interactive follow up
				cmd.Flags().Set("chat", "false")
				nextPrompt = args[1:]
			} else {
				// Interactive mode
				cmd.Flags().Set("chat", "true")
			}

			resumedMessages = llmMsgs
			resumedSessionUUID = uuid

			return runLLMChat(cmd, nextPrompt)
		},
	}
	resumeCmd.Flags().AddFlagSet(rootCmd.Flags())
	rootCmd.AddCommand(resumeCmd)

	// Initialize History Manager
	home, _ := os.UserHomeDir()
	histDir := filepath.Join(home, ".config/llmcli")
	os.MkdirAll(histDir, 0755)

	hm, err := history.New(filepath.Join(histDir, "history.db"), filepath.Join(histDir, "history.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to init history: %v\n", err)
	} else {
		historyMgr = hm
		defer hm.Close()
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var resumedMessages []Message
var resumedSessionUUID string

func markChatStart(session *Session, userMsg, systemPrompt, model string, seed int, temperature *float64, apiBase string, maxTokens int, jsonMode bool, stopSequences interface{}, extraParams string, jsonSchema string, reasoningEffort string, reasoningMaxTokens int, reasoningExclude bool) error {
	if historyMgr == nil {
		return nil
	}
	event := history.SessionStartEvent{
		SID:                session.UUID,
		TS:                 time.Now().Unix(),
		UserMsg:            userMsg,
		SystemPrompt:       systemPrompt,
		Model:              model,
		Seed:               seed,
		Temperature:        temperature,
		APIBase:            apiBase,
		MaxTokens:          maxTokens,
		JSONMode:           jsonMode,
		StopSequences:      stopSequences,
		ExtraParams:        extraParams,
		JsonSchema:         jsonSchema,
		ReasoningEffort:    reasoningEffort,
		ReasoningMaxTokens: reasoningMaxTokens,
		ReasoningExclude:   reasoningExclude,
	}
	return historyMgr.SaveSessionStart(event)
}

func getFirstEnv(fallback string, envVars ...string) string {
	// Also check for common API key environment variables that might contain model info
	additionalVars := []string{
		"OPENAI_API_MODEL",
		"GROQ_API_MODEL",
		"LLM_MODEL",
		"ANTHROPIC_API_MODEL", // Add Anthropic for completeness
	}

	allVars := append(envVars, additionalVars...)

	for _, env := range allVars {
		v := os.Getenv(env)
		if v != "" {
			return v
		}
	}
	return fallback
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	// Staff SWE approximation: Words * 1.3 usually covers common code/English token density
	// but requirement asks for wordcount approximation.
	return len(strings.Fields(text))
}

func runLLMChat(cmd *cobra.Command, args []string) error {
	// Check for --session flag alias
	isSession, _ := cmd.Flags().GetBool("session")
	if isSession {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return runSession(cmd, args, cfg)
	}

	preRunTime := time.Now()

	// Handle Resume
	var session *Session
	if resumedSessionUUID != "" {
		session = &Session{UUID: resumedSessionUUID, Timestamp: time.Now()}
	} else {
		session = newSession()
	}

	modelname, _ := cmd.Flags().GetString("model")

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// FIX: Check if model was explicitly provided via flag
	modelFlagProvided := cmd.Flags().Changed("model")

	if !modelFlagProvided && len(modelname) == 0 {
		if cfg.Default != "" {
			modelname = cfg.Default
		} else {
			modelname = getFirstEnv("gpt-3.5-turbo", "OPENAI_API_MODEL", "GROQ_API_MODEL", "LLM_MODEL")
		}
	}

	seed, _ := cmd.Flags().GetInt("seed")
	// temperature handled via RunConfig
	apiKey, _ := cmd.Flags().GetString("api-key")
	apiBase, _ := cmd.Flags().GetString("api-base")
	stream, _ := cmd.Flags().GetBool("stream")
	verbose, _ := cmd.Flags().GetBool("verbose")
	chat, _ := cmd.Flags().GetBool("chat")
	chat_send, _ := cmd.Flags().GetBool("chat-send")
	systemPrompt, _ := cmd.Flags().GetString("prompt")
	debug, _ := cmd.Flags().GetBool("debug")
	maxTokens, _ := cmd.Flags().GetInt("max_tokens")
	jsonMode, _ := cmd.Flags().GetBool("json")
	extraParams, _ := cmd.Flags().GetString("extra")
	jsonSchema, _ := cmd.Flags().GetString("json-schema")

	// Reasoning flags
	noReasoning, _ := cmd.Flags().GetBool("no-reasoning")
	reasoningLow, _ := cmd.Flags().GetBool("reasoning-low")
	reasoningMedium, _ := cmd.Flags().GetBool("reasoning-medium")
	reasoningHigh, _ := cmd.Flags().GetBool("reasoning-high")
	reasoningXHigh, _ := cmd.Flags().GetBool("reasoning-xhigh")
	reasoningMax, _ := cmd.Flags().GetInt("reasoning-max")
	reasoningExclude, _ := cmd.Flags().GetBool("reasoning-exclude")

	// Clipboard flags
	useClipboard, _ := cmd.Flags().GetBool("clipboard")
	contextOrder, _ := cmd.Flags().GetString("context-order")
	pipedWrapper, _ := cmd.Flags().GetString("piped-wrapper")

	contextArmor, _ := cmd.Flags().GetBool("context-armor")
	if !cmd.Flags().Changed("context-armor") && cfg.ContextArmor != nil {
		contextArmor = *cfg.ContextArmor
	}
	imageLog, _ := cmd.Flags().GetBool("image-log")
	if cmd.Flags().Changed("no-image-log") {
		if noImageLog, _ := cmd.Flags().GetBool("no-image-log"); noImageLog {
			imageLog = false
		}
	}

	// Shell Assistant
	shellMode, _ := cmd.Flags().GetBool("shell")
	if shellMode {
		return runShellAssistant(cmd, args, cfg)
	}

	// Shell History Context
	historyCount, _ := cmd.Flags().GetInt("history")
	var historyContext string
	if historyCount > 0 {
		shellInfo := detectShell()
		cmds, err := readShellHistory(shellInfo, historyCount)
		if err != nil {

			if verbose {
				fmt.Printf("Warning: failed to read shell history: %v\n", err)
			}
		} else if len(cmds) > 0 {
			var sb strings.Builder
			sb.WriteString("\n<user-shell-history>\n")
			for i := 0; i < len(cmds); i++ {
				idx := i
				cmd := cmds[i]
				cmd = strings.ReplaceAll(cmd, "&", "&amp;")
				cmd = strings.ReplaceAll(cmd, "<", "&lt;")
				cmd = strings.ReplaceAll(cmd, ">", "&gt;")
				cmd = strings.ReplaceAll(cmd, "\"", "&quot;")

				attrs := ""
				if idx == 0 {
					attrs = " oldest=\"true\""
				}
				if idx == len(cmds)-1 {
					attrs += " newest=\"true\""
				}
				sb.WriteString(fmt.Sprintf("<item index=\"%d\"%s><input>%s</input></item>\n", idx, attrs, cmd))
			}
			sb.WriteString("</user-shell-history>\n")
			historyContext = sb.String()
		}
	}

	var configExtraBody map[string]interface{}

	// Apply config profile overrides if modelname matches a profile and flag not explicitly set
	// Resolve configuration
	runCfg, err := getRunConfig(cmd, cfg, modelname)
	if err != nil {
		log.Printf("Warning: failed to resolve config: %v", err)
	}

	// Apply resolved config
	modelname = runCfg.ModelName
	apiKey = runCfg.ApiKey
	apiBase = runCfg.ApiBase
	temperature := runCfg.Temperature
	seed = runCfg.Seed
	maxTokens = runCfg.MaxTokens
	contextOrder = runCfg.ContextOrder
	configExtraBody = runCfg.ExtraBody

	// Apply reasoning settings
	reasoningMax = runCfg.ReasoningMaxTokens
	reasoningExclude = runCfg.ReasoningExclude

	// Reset reasoning flags based on resolved config
	noReasoning = false
	reasoningLow = false
	reasoningMedium = false
	reasoningHigh = false
	reasoningXHigh = false

	switch runCfg.ReasoningEffort {
	case "none":
		noReasoning = true
	case "low":
		reasoningLow = true
	case "medium":
		reasoningMedium = true
	case "high":
		reasoningHigh = true
	case "xhigh":
		reasoningXHigh = true
	}

	// Apply top-level piped_input_wrapper from config if flag not explicitly set
	if cfg.PipedInputWrapper != nil && !cmd.Flags().Changed("piped-wrapper") {
		pipedWrapper = *cfg.PipedInputWrapper
	}

	stopSequences, _ := cmd.Flags().GetString("stop")
	var stopSeqInterface interface{}
	if strings.HasPrefix(stopSequences, "[") && strings.HasSuffix(stopSequences, "]") {
		var stopSeqArray []string
		err := json.Unmarshal([]byte(stopSequences), &stopSeqArray)
		if err != nil {
			log.Fatal(err)
		}
		stopSeqInterface = stopSeqArray
	} else {
		stopSeqInterface = stopSequences
	}

	messages := make([]Message, 0)

	// If resuming, use loaded messages
	if len(resumedMessages) > 0 {
		messages = resumedMessages
	} else {
		if len(strings.TrimSpace(systemPrompt)) > 0 {
			messages = append(messages, *NewMessage("system", systemPrompt))
		}
	}

	// === File Context Resolution ===
	autoFlag, _ := cmd.Flags().GetBool("auto")
	filesFlag, _ := cmd.Flags().GetStringSlice("files")
	contextFormat, _ := cmd.Flags().GetString("context-format")
	showFilenames, _ := cmd.Flags().GetString("show-filenames")
	cwd, _ := os.Getwd()

	// Construct initial user message to parse for @ tokens
	var usermsg string = strings.Join(args, " ")

	resolver := NewPathResolver(verbose)
	cleanedPrompt, atPaths := resolver.ParsePrompt(usermsg)

	// If we found @ tokens, update the user message to the cleaned version
	if len(atPaths) > 0 {
		usermsg = cleanedPrompt
	}

	allPaths := append(filesFlag, atPaths...)

	noGitignore, _ := cmd.Flags().GetBool("no-gitignore")
	noDefaultIgnore, _ := cmd.Flags().GetBool("no-ignored-files")

	// Resolve paths (expand globs, git aliases, etc)
	resolvedPaths, err := resolver.Resolve(allPaths, !noGitignore, !noDefaultIgnore)
	if err != nil {
		return err
	}

	// Auto-Mode
	if autoFlag {
		// Initialize indexer
		ignoredDirs := []string{".git", "node_modules", "dist", "vendor", "__pycache__"}
		maxRepoFiles := 1000
		if cfg.Context != nil {
			if len(cfg.Context.IgnoredDirs) > 0 {
				ignoredDirs = cfg.Context.IgnoredDirs
			}
			if cfg.Context.MaxRepoFiles != nil {
				maxRepoFiles = *cfg.Context.MaxRepoFiles
			}
		}

		indexer := NewRepoIndexer(ignoredDirs, maxRepoFiles, verbose)
		repoMap, err := indexer.GenerateRepoMap(".")
		if err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "Warning: failed to generate repo map: %v\n", err)
			}
		} else {
			// Initialize selector
			selector := NewAutoSelector(verbose)
			selectorModel := modelname
			if cfg.Context != nil && cfg.Context.AutoSelectorModel != nil && *cfg.Context.AutoSelectorModel != "" {
				selectorModel = *cfg.Context.AutoSelectorModel
			}

			// Start pulsating magenta color animation (Bubble Tea style)
			stopAnimation := make(chan bool)
			var animWg sync.WaitGroup
			animWg.Add(1)
			go func() {
				defer animWg.Done()
				// Magenta color gradient for smooth pulsation (256-color ANSI)
				// Creates breathing effect from dim to bright magenta
				colorFrames := []string{
					"\033[38;5;126m", // Dim magenta
					"\033[38;5;162m", // Medium-dim magenta
					"\033[38;5;198m", // Medium magenta
					"\033[38;5;199m", // Medium-bright magenta
					"\033[38;5;200m", // Bright magenta
					"\033[38;5;201m", // Very bright magenta
					"\033[38;5;200m", // Bright magenta (reverse)
					"\033[38;5;199m", // Medium-bright magenta (reverse)
					"\033[38;5;198m", // Medium magenta (reverse)
					"\033[38;5;162m", // Medium-dim magenta (reverse)
				}
				frameIdx := 0
				reset := "\033[0m"
				for {
					select {
					case <-stopAnimation:
						fmt.Fprintf(os.Stderr, "\r\033[K") // Clear line
						return
					case <-time.After(120 * time.Millisecond):
						color := colorFrames[frameIdx]
						fmt.Fprintf(os.Stderr, "\r%s■ analyzing...%s", color, reset)
						frameIdx = (frameIdx + 1) % len(colorFrames)
					}
				}
			}()

			// Use resolved API key/base for selector if needed, or default to main
			// Note: We use the main model's config for the selector for now
			autoPaths, err := selector.SelectFiles(usermsg, repoMap, selectorModel, apiKey, apiBase, debug)

			// Stop animation
			close(stopAnimation)
			animWg.Wait()

			if err != nil {
				if verbose {
					fmt.Fprintf(os.Stderr, "Warning: auto-selection failed: %v\n", err)
				}
			} else {
				resolvedPaths = append(resolvedPaths, autoPaths...)

				// Display loaded files in magenta
				if len(autoPaths) > 0 {
					magenta := "\033[35m"
					reset := "\033[0m"
					fmt.Fprintf(os.Stderr, "%sreviewed: %s%s\n", magenta, strings.Join(autoPaths, ", "), reset)
				}
			}
		}
	}

	// === Context Collection ===
	var contextBuilder strings.Builder
	var debugContextBuilder strings.Builder
	var collectedImages []string
	hasContext := false

	// 0. File Context
	if len(resolvedPaths) > 0 {
	maxSizeKB := 1024
	if cfg.Context != nil && cfg.Context.MaxFileSizeKB != nil {
		maxSizeKB = *cfg.Context.MaxFileSizeKB
	}

	maxImageSizeKB := 10240 // default 10MB
	if cfg.Context != nil && cfg.Context.MaxImageSizeKB != nil {
		maxImageSizeKB = *cfg.Context.MaxImageSizeKB
	}

		loader := NewFileLoader(maxSizeKB, maxImageSizeKB, verbose)
		fileContexts, err := loader.LoadAll(resolvedPaths)
		if err != nil {
			return err
		}

		if len(fileContexts) > 0 {
			// Full context for LLM
			// If armor is on, we don't want formatContext (XML) to include outer tags, we'll do it globally
				formattedContext, images := formatContext(fileContexts, contextFormat, showFilenames, cwd, -1, !contextArmor)
				contextBuilder.WriteString(formattedContext + "\n")
				collectedImages = append(collectedImages, images...)

				// Preview images in terminal (if enabled and supported)
				if imageLog && len(collectedImages) > 0 && is_interactive(os.Stdout.Fd()) {
					if detectTerminalImageSupport() {
						fmt.Fprintf(os.Stderr, "\033[36m[Image Preview]\033[0m Sending %d image(s) to model:\n", len(collectedImages))
						for i, img := range collectedImages {
							if err := displayImageInTerminal(img, 400); err != nil {
								fmt.Fprintf(os.Stderr, "\033[33mWarning: failed to display image %d: %v\033[0m\n", i+1, err)
							} else {
								fmt.Fprintln(os.Stderr)
							}
						}
					}
				}

			// Truncated context for debug output
			truncateLimit := 10 // Default 10 lines
			if cfg.Context != nil && cfg.Context.DebugTruncateFiles != nil {
				truncateLimit = *cfg.Context.DebugTruncateFiles
			}
			debugFormattedContext, _ := formatContext(fileContexts, contextFormat, showFilenames, cwd, truncateLimit, !contextArmor)
			debugContextBuilder.WriteString(debugFormattedContext + "\n")

			hasContext = true
		}
	}

	// 1. Piped Input
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		var pipedContent strings.Builder
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			pipedContent.WriteString(scanner.Text())
			pipedContent.WriteString("\n")
		}

		if pipedContent.Len() > 0 {
			content := strings.TrimRight(pipedContent.String(), "\n")
			if pipedWrapper != "" {
				// If armor is on and wrapper is "context", we avoid double wrapping
				if contextArmor && pipedWrapper == "context" {
					contextBuilder.WriteString(content + "\n")
					debugContextBuilder.WriteString(content + "\n")
				} else {
					// Wrap with custom or default wrapper
					formatted := fmt.Sprintf("<%s>\n%s\n</%s>\n", pipedWrapper, content, pipedWrapper)
					contextBuilder.WriteString(formatted)
					debugContextBuilder.WriteString(formatted)
				}
			} else {
				// No wrapper - just raw content
				contextBuilder.WriteString(content + "\n")
				debugContextBuilder.WriteString(content + "\n")
			}
			hasContext = true
		}
	}

	// 2. Clipboard
	if useClipboard {
		clipboardCmd := exec.Command("pbpaste")
		clipboardOutput, err := clipboardCmd.Output()
		if err != nil {
			log.Printf("Warning: failed to read clipboard: %v", err)
		} else {
			clipboardContent := string(clipboardOutput)
			if len(clipboardContent) > 0 {
				formatted := fmt.Sprintf("<clipboard>\n%s\n</clipboard>\n", clipboardContent)
				contextBuilder.WriteString(formatted)
				debugContextBuilder.WriteString(formatted)
				hasContext = true
			}
		}
	}

	// 3. Shell History
	if historyContext != "" {
		// historyContext already has tags <user-shell-history> (not context-user-shell-history)
		contextBuilder.WriteString(historyContext)
		debugContextBuilder.WriteString(historyContext)
		hasContext = true
	}

	// === Construct Final Message ===
	// usermsg is already defined above
	var debugUsermsg string = usermsg

	if hasContext {
		fullContext := strings.TrimSpace(contextBuilder.String())
		debugContext := strings.TrimSpace(debugContextBuilder.String())

		if contextArmor {
			fullContext = "<context>\n" + fullContext + "\n</context>"
			debugContext = "<context>\n" + debugContext + "\n</context>"
		}

		if contextOrder == "append" {
			if len(usermsg) > 0 {
				usermsg = usermsg + "\n\n" + fullContext
				debugUsermsg = debugUsermsg + "\n\n" + debugContext
			} else {
				usermsg = fullContext
				debugUsermsg = debugContext
			}
		} else {
			// Default: prepend
			if len(usermsg) > 0 {
				usermsg = fullContext + "\n\n" + usermsg
				debugUsermsg = debugContext + "\n\n" + debugUsermsg
			} else {
				usermsg = fullContext
				debugUsermsg = debugContext
			}
		}
	}

	apiKey, apiBase, err = resolveLLMApi(apiKey, apiBase)
	if err != nil {
		log.Fatal(err)
	}

	dryRun, _ := cmd.Flags().GetBool("dry")
	timingEnabled, _ := cmd.Flags().GetBool("vt")

	if verbose && !dryRun && !debug {
		timeout := 1 * time.Second
		models, err := getModelList(apiKey, apiBase, timeout)
		if err == nil {
			for _, model := range models {
				fmt.Println(model.ID, model.Meta)
			}
		}
	}

	// Determine reasoning configuration with mutual exclusivity handling
	var reasoningConfiguredMax int
	var reasoningConfiguredExclude bool
	reasoningFlagCount := 0

	if noReasoning {
		reasoningFlagCount++
	}
	if reasoningLow {
		reasoningFlagCount++
	}
	if reasoningMedium {
		reasoningFlagCount++
	}
	if reasoningHigh {
		reasoningFlagCount++
	}
	if reasoningXHigh {
		reasoningFlagCount++
	}
	if reasoningMax > 0 {
		reasoningConfiguredMax = reasoningMax
	}

	// Warn if multiple reasoning flags were specified
	if reasoningFlagCount > 1 {
		fmt.Fprintf(os.Stderr, "Warning: Multiple reasoning effort flags specified, using last-specified option\n")
	}

	reasoningConfiguredExclude = reasoningExclude

	if debug || dryRun {
		sTokens := estimateTokens(systemPrompt)
		pTokens := estimateTokens(usermsg)
		cTokens := estimateTokens(contextBuilder.String())
		fmt.Printf("\n--- LLM Request Stats ---\n")
		fmt.Printf("Model:  %s\n", modelname)
		fmt.Printf("Tokens: System: ~%d | Prompt: ~%d | Context: ~%d | Images: %d | Total: ~%d\n",
			sTokens, pTokens, cTokens, len(collectedImages), sTokens+pTokens+cTokens+(len(collectedImages)*1000))
		tempVal := 0.0
		if temperature != nil {
			tempVal = *temperature
		}
		fmt.Printf("Params: Temp: %.2f | Seed: %d | MaxTokens: %d\n", tempVal, seed, maxTokens)
		if runCfg.ReasoningEffort != "" {
			fmt.Printf("Reasoning: Effort: %s | Max: %d | Exclude: %v\n",
				runCfg.ReasoningEffort, runCfg.ReasoningMaxTokens, runCfg.ReasoningExclude)
		}

		if debug || (dryRun && verbose) {
			fmt.Printf("\nPROMPT:\n%s\n\nSYSTEM MESSAGE:\n%s\n", debugUsermsg, systemPrompt)
		}

	}

	// Only mark start if new session
	if resumedSessionUUID == "" && !dryRun {
		markChatStart(session, usermsg, systemPrompt, modelname, seed, temperature, apiBase, maxTokens, jsonMode, stopSeqInterface, extraParams, jsonSchema, runCfg.ReasoningEffort, reasoningConfiguredMax, reasoningConfiguredExclude)
	}

	var extra map[string]interface{}

	extraParamsMap := map[string]interface{}{}
	if err := json.Unmarshal([]byte(extraParams), &extraParamsMap); err != nil {
		return fmt.Errorf("failed to parse extra params JSON: %w", err)
	}

	extra = map[string]interface{}{}
	if maxTokens > 0 {
		extra["max_tokens"] = maxTokens
	}

	// Use flat reasoning_effort for Chat Completions (OpenAI/New Standard)
	// We use the runCfg value which is the source of truth after flag/config resolution
	if runCfg.ReasoningEffort != "" && runCfg.ReasoningEffort != "none" {
		extra["reasoning_effort"] = runCfg.ReasoningEffort
	}

	// Handle legacy/OpenRouter specific fields via reasoning object
	// We construct this separately because some providers (OpenRouter) might want
	// both reasoning_effort (flat) AND extra parameters in a reasoning object,
	// or reasoning_effort might be ignored if reasoning object is present depending on the provider.
	// But based on user requirements to support exclude, we must send it.
	reasoningObj := make(map[string]interface{})
	if reasoningConfiguredMax > 0 {
		reasoningObj["max_tokens"] = reasoningConfiguredMax
	}
	if reasoningConfiguredExclude {
		reasoningObj["exclude"] = true
	}

	if len(reasoningObj) > 0 {
		extra["reasoning"] = reasoningObj
	}

	// Add verbosity if set
	if runCfg.Verbosity != "" {
		extra["verbosity"] = runCfg.Verbosity
	}

	switch v := stopSeqInterface.(type) {
	case string:
		if v != "" {
			extra["stop"] = v
		}
	case []string:
		if len(v) > 0 {
			extra["stop"] = v
		}
	default:
	}

	if len(jsonSchema) > 0 {
		jsonSchemaObj := map[string]interface{}{}
		if err := json.Unmarshal([]byte(jsonSchema), &jsonSchemaObj); err != nil {
			log.Fatal(err)
		}
		extra["json_schema"] = jsonSchemaObj
	} else if jsonMode {
		extra["response_format"] = map[string]interface{}{"type": "json_object"}
	}

	for k, v := range configExtraBody {
		extra[k] = v
	}

	// CLI params override config
	for k, v := range extraParamsMap {
		extra[k] = v
	}

	if dryRun {
		// Construct full payload for display
		displayMessages := make([]Message, len(messages))
		copy(displayMessages, messages)
		if len(usermsg) > 0 {
			msg := NewMessage("user", usermsg)
			msg.Images = collectedImages
			displayMessages = append(displayMessages, *msg)
		}

		req := LLMChatRequestBasic{
			Model:       modelname,
			Seed:        seed,
			Temperature: temperature,
			Stream:      stream,
			Messages:    make([]LLMMessage, len(displayMessages)),
		}
		// convert messages
		for i, m := range displayMessages {
			if len(m.Images) > 0 {
				parts := []ContentPart{
					{Type: "text", Text: m.Content},
				}
				for _, img := range m.Images {
					parts = append(parts, ContentPart{
						Type: "image_url",
						ImageUrl: &ImageUrl{Url: img},
					})
				}
				req.Messages[i] = LLMMessage{Role: m.Role, Content: parts}
			} else {
				req.Messages[i] = LLMMessage{Role: m.Role, Content: m.Content}
			}
		}

		mergedData := map[string]interface{}{}
		reqJson, _ := json.Marshal(req)
		json.Unmarshal(reqJson, &mergedData)
		for k, v := range extra {
			mergedData[k] = v
		}

		fmt.Printf("\nAPI Base: %s\n", apiBase)
		jsonBytes, _ := json.MarshalIndent(mergedData, "", "  ")
		fmt.Printf("Request Body:\n%s\n", string(jsonBytes))

		return nil
	}

	// Create context for LLM cancellation with configured timeout
	ctx, cancel := context.WithTimeout(context.Background(), runCfg.Timeout)
	defer cancel()

	llmApiFunc := func(messages []Message) (<-chan StreamEvent, error) {
		filteredMessages := make([]LLMMessage, len(messages))
		for i, msg := range messages {
			if len(msg.Images) > 0 {
				parts := []ContentPart{
					{Type: "text", Text: msg.Content},
				}
				for _, img := range msg.Images {
					parts = append(parts, ContentPart{
						Type: "image_url",
						ImageUrl: &ImageUrl{Url: img},
					})
				}
				filteredMessages[i] = LLMMessage{
					Role:    msg.Role,
					Content: parts,
				}
			} else {
				filteredMessages[i] = LLMMessage{
					Role:    msg.Role,
					Content: msg.Content,
				}
			}
		}
		return llmChat(ctx, filteredMessages, modelname, seed, temperature, nil, apiKey, apiBase, stream, extra, verbose)
	}

	llmHistoryFunc := func(msg Message) error {
		if historyMgr == nil {
			return nil
		}
		data := history.MessageEvent{
			ID:  msg.UUID,
			SID: session.UUID,
			TS:  time.Now().Unix(),
			Message: history.ChatMessage{
				UUID:    msg.UUID,
				Role:    msg.Role,
				Content: msg.Content,
				Images:  msg.Images,
			},
		}
		return historyMgr.SaveMessage(data)
	}

	if len(usermsg) == 0 || chat || chat_send {

		var initialTextareaValue = ""

		if len(usermsg) > 0 {
			initialTextareaValue = usermsg
		}

		p := tea.NewProgram(initialModel(*session, messages, llmHistoryFunc, llmApiFunc, initialTextareaValue, chat_send, modelname), // use the full size of the terminal in its "alternate screen buffer"
			tea.WithMouseCellMotion())

		if _, err := p.Run(); err != nil {
			log.Println(err)
			return err
		}

		return nil
	}

	if len(usermsg) > 0 {
		msg := NewMessage("user", usermsg)
		msg.Images = collectedImages
		messages = append(messages, *msg)
	}

	var timings Timings
	if debug {
		timings.BinaryStartup = preRunTime.Sub(startTime)
	}

	llmCallStartTime := time.Now()
	ch, err := llmApiFunc(messages)

	if debug {
		timings.TimeToFirstLLMCall = llmCallStartTime.Sub(startTime)
	}

	if err != nil {
		fmt.Println(err)
		return err
	}

	firstChunk := true

	// Reasoning UI state
	logReasoning := true
	if cfg.LogReasoning != nil {
		logReasoning = *cfg.LogReasoning
	}
	shorten := -1
	if cfg.LogReasoningShorten != nil {
		shorten = *cfg.LogReasoningShorten
	}

	thinkingStart := "\033[32m<thinking>\033[0m\n"
	if cfg.ThinkingStartTag != nil {
		thinkingStart = *cfg.ThinkingStartTag
	}
	thinkingEnd := "\n\033[32m</thinking>\033[0m\n"
	if cfg.ThinkingEndTag != nil {
		thinkingEnd = *cfg.ThinkingEndTag
	}

	var reasoningBuffer string
	var displayedLines int
	var thinkingPrinted bool
	var reasoningDone bool

	// Output accumulation for --save-to
	var contentBuffer strings.Builder
	hasReasoning := false

	// Timing Metrics
	t_start := time.Now()
	var t_ttft, t_reasoning_end time.Time
	var ttft_set = false
	var tokens_gen int

	for event := range ch {
		if firstChunk {
			if debug {
				timings.TimeToFirstChunk = time.Since(startTime)
			}
			firstChunk = false
		}

		if event.Type == "reasoning" {
			hasReasoning = true
			if !ttft_set {
				t_ttft = time.Now()
				ttft_set = true
			}

			// Always track reasoning end time, even if not logging
			// We update it on every reasoning token, the last one will be the end
			t_reasoning_end = time.Now()

			if !logReasoning {
				continue
			}
			if !thinkingPrinted {
				fmt.Print(thinkingStart)
				thinkingPrinted = true
			}
			if shorten > 0 {
				reasoningBuffer += event.Content
				lines := strings.Split(strings.TrimRight(reasoningBuffer, "\n"), "\n")

				start := 0
				if len(lines) > shorten {
					start = len(lines) - shorten
				}
				toDisplay := lines[start:]

				if displayedLines > 0 {
					fmt.Printf("\033[%dA", displayedLines) // Move up
					fmt.Printf("\033[J")                   // Clear down
				}

				for _, line := range toDisplay {
					fmt.Println(line)
				}
				displayedLines = len(toDisplay)
			} else {
				fmt.Print(event.Content)
			}
		} else if event.Type == "content" {
			if !ttft_set {
				t_ttft = time.Now()
				ttft_set = true
			}

			// Handle transition from reasoning to content
			if hasReasoning && !reasoningDone {
				// We might have updated t_reasoning_end in the loop above
				// If we are logging, we print the end tag
				if logReasoning && thinkingPrinted {
					fmt.Print(thinkingEnd)
				}
				reasoningDone = true
			}

			tokens_gen += estimateTokens(event.Content)
			fmt.Print(event.Content)
			contentBuffer.WriteString(event.Content)
		}
	}

	// Save to file if requested
	saveTo, _ := cmd.Flags().GetString("save-to")
	if saveTo != "" {
		finalContent := contentBuffer.String()
		if hasReasoning && !t_reasoning_end.IsZero() && !t_ttft.IsZero() {
			duration := t_reasoning_end.Sub(t_ttft)
			durStr := fmt.Sprintf("%dm%ds", int(duration.Minutes()), int(duration.Seconds())%60)
			header := fmt.Sprintf("< thought for %s >\n\n", durStr)
			finalContent = header + finalContent
		}
		if err := saveOutput(saveTo, finalContent, modelname); err != nil {
			fmt.Fprintf(os.Stderr, "\nWarning: failed to save output: %v\n", err)
		}
	}

	if timingEnabled {
		t_done := time.Now()
		ttft_ms := t_ttft.Sub(t_start).Seconds()
		total_s := t_done.Sub(t_start).Seconds()

		var r_s float64
		if !t_reasoning_end.IsZero() {
			r_s = t_reasoning_end.Sub(t_ttft).Seconds()
		}

		gen_s := t_done.Sub(t_ttft).Seconds() - r_s
		prefill_tps := float64(estimateTokens(usermsg)+estimateTokens(contextBuilder.String())) / ttft_ms
		gen_tps := float64(tokens_gen) / gen_s

		fmt.Printf("\n---\n")
		fmt.Printf("TTFT: %.3fs | Reasoning: %.3fs | Gen: %.3fs | TPS_Prefill: %.1f | TPS_Gen: %.1f | ∑: %.1fs \n",
			ttft_ms, r_s, gen_s, prefill_tps, gen_tps, total_s)
	}

	if debug {
		timings.TimeToComplete = time.Since(startTime)
		displayTimings(timings)
	}

	return nil
}

type chatTuiState struct {
	spin           bool
	streaming      bool
	spinner        spinner.Model
	viewport       viewport.Model
	textarea       textarea.Model
	llmMessages    []Message
	llmApi         func(messages []Message) (<-chan StreamEvent, error)
	historyApi     func(Message) error
	session        Session
	ch             <-chan StreamEvent
	err            error
	renderMarkdown bool
	viewportWidth  int
	mdPaddingWidth int
	shift          bool
	sendRightAway  bool
	modelName      string

	// Reasoning
	isReasoning    bool
	reasoningText  string
	reasoningStart time.Time

	// Search Mode
	inSearch   bool
	searchList list.Model
}

type item struct {
	title, desc, uuid string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }
func (i item) FilterValue() string { return i.title + " " + i.desc }

func getLastMsg(m chatTuiState) (Message, error) {
	if len(m.llmMessages) == 0 {
		return Message{}, errors.New("no messages in history")
	}
	return m.llmMessages[len(m.llmMessages)-1], nil
}

func initialModel(session Session, messages []Message, llmHistoryApi func(Message) error, llmApi func(messages []Message) (<-chan StreamEvent, error), initialTextareaValue string, sendRightAway bool, modelName string) chatTuiState {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Focus()

	ta.Prompt = "┃ "
	ta.CharLimit = 100000
	ta.MaxHeight = 32
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false
	// Violet cursor when reasoning/active
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("57"))

	vp := viewport.New(32, 12)
	vp.SetContent(`<llm chat history is empty>`)
	// vp.HighPerformanceRendering = true
	vp.MouseWheelEnabled = true
	ta.KeyMap.InsertNewline.SetEnabled(false)

	ta.SetValue(initialTextareaValue)

	if len(messages) > 0 {
		vp.SetContent(formatMessageLog(messages, true, 80, 0, "", "", true, modelName))
	}
	vp.GotoBottom()

	sp := spinner.New()

	// Initialize Search List
	searchList := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	searchList.Title = "History Search"
	searchList.SetShowHelp(false)

	return chatTuiState{
		spin:           false,
		streaming:      false,
		spinner:        sp,
		textarea:       ta,
		viewport:       vp,
		llmMessages:    messages,
		llmApi:         llmApi,
		historyApi:     llmHistoryApi,
		session:        session,
		ch:             nil,
		err:            nil,
		renderMarkdown: true,
		viewportWidth:  80,
		mdPaddingWidth: 0,
		sendRightAway:  sendRightAway,
		searchList:     searchList,
		modelName:      modelName,
	}
}

func (m chatTuiState) Init() tea.Cmd {
	return tea.Batch(textarea.Blink)
}

func removeLastMsg(m chatTuiState) error {
	for len(m.llmMessages) > 0 {
		lastMsg, err := getLastMsg(m)
		if err != nil {
			return err
		}

		if lastMsg.Role == "assistant" {
			break
		}

		payload, _ := json.Marshal(map[string]string{
			"sysop": "remove_msg",
			"id":    lastMsg.UUID,
		})
		pseudoMsg := NewMessage("__sys__", string(payload))
		m.historyApi(*pseudoMsg)

		m.llmMessages = m.llmMessages[:len(m.llmMessages)-1]
	}

	if len(m.llmMessages) > 0 {
		lastMsg, err := getLastMsg(m)
		if err != nil {
			return err
		}

		payload, _ := json.Marshal(map[string]string{
			"sysop": "remove_msg",
			"id":    lastMsg.UUID,
		})
		pseudoMsg := NewMessage("__sys__", string(payload))
		m.historyApi(*pseudoMsg)

		m.llmMessages = m.llmMessages[:len(m.llmMessages)-1]
	}

	return nil
}

var markdownCache = struct {
	sync.Mutex
	cache map[string]string
}{cache: make(map[string]string)}

func formatMessageLog(msgs []Message, renderMarkdown bool, lineWidth int,
	mdPadding int, suffix string, roleFormat string, renderNewlinesInUsermsgs bool, modelName string) string {

	roleFmt := "### %s:\n"
	if roleFormat != "" {
		roleFmt = roleFormat
	}

	var ret strings.Builder

	for i, msg := range msgs {
		content := strings.TrimRight(msg.Content, " \t\r\n")

		// Customize header for Assistant
		currentRoleFmt := roleFmt
		roleDisplay := strings.ToUpper(msg.Role)
		if msg.Role == "assistant" && modelName != "" {
			roleDisplay = fmt.Sprintf("ASSISTANT - %s", modelName)
		}

		if msg.Role == "user" && renderNewlinesInUsermsgs {
			re := regexp.MustCompile(`(?m:^(  |\z)|\n)`)
			content = re.ReplaceAllStringFunc(content, func(match string) string {
				if strings.HasPrefix(match, "  ") || match == "\n" {
					return match
				}
				return "  \n"
			})
		}

		if renderMarkdown {
			key := fmt.Sprintf("%s__%d__%d", content, lineWidth, mdPadding)
			markdownCache.Lock()
			if cachedContent, ok := markdownCache.cache[key]; ok {
				markdownCache.Unlock()
				content = cachedContent
			} else {
				renderedContent := markdown.Render(content, lineWidth, mdPadding)
				markdownCache.cache[key] = string(renderedContent)
				markdownCache.Unlock()
				content = string(renderedContent)
			}
		}

		content = strings.TrimRight(content, " \t\r\n")

		sfx := ""
		if i == len(msgs)-1 && len(suffix) > 0 {
			sfx = suffix
		}

		fmt.Fprintf(&ret, currentRoleFmt+"%s%s\n\n", roleDisplay, content, sfx)
	}

	return ret.String()
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second*1, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func sendMsg(m chatTuiState, usermsg string) (tea.Model, tea.Cmd) {
	var newmsg = *NewMessage("user", usermsg)

	m.llmMessages = append(m.llmMessages, newmsg)
	m.historyApi(newmsg)

	ch, err := m.llmApi(m.llmMessages)

	if err != nil {
		log.Println(err)
		m.err = err
		return m, nil
	}

	m.llmMessages = append(m.llmMessages, *NewMessage("assistant", ""))

	m.spin = true
	m.spinner.Spinner = spinner.Pulse
	m.spinner.Spinner.FPS = time.Second / 10
	m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("171"))

	m.ch = ch
	m.textarea.Reset()
	m.textarea.Placeholder = TEXTINPUT_PLACEHOLDER
	m.textarea.Focus()

	m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, m.spinner.View(), "", true, m.modelName))
	m.viewport.GotoBottom()

	return m, tea.Batch(m.spinner.Tick, readLLMResponse(m, m.ch))
}

func (m chatTuiState) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
		spCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	if m.sendRightAway {
		m.sendRightAway = false
		var usermsg = m.textarea.Value()
		_m, cmds := sendMsg(m, usermsg)
		return _m, tea.Batch(tiCmd, vpCmd, cmds)
	}

	if m.inSearch {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if msg.Type == tea.KeyEsc || msg.Type == tea.KeyCtrlC {
				m.inSearch = false
				return m, nil
			}
			if msg.Type == tea.KeyEnter {
				selectedItem := m.searchList.SelectedItem()
				if selectedItem != nil {
					i := selectedItem.(item)
					// Resume Logic in TUI:
					// Load messages for uuid
					if historyMgr != nil {
						msgs, err := historyMgr.GetSessionMessages(i.uuid)
						if err == nil {
							// Clear current
							m.llmMessages = []Message{}
							for _, mg := range msgs {
								m.llmMessages = append(m.llmMessages, Message{
									Role: mg.Role, Content: mg.Content, UUID: mg.UUID,
								})
							}
							m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, "", "", true, m.modelName))
							m.viewport.GotoBottom()
						}
					}
				}
				m.inSearch = false
				return m, nil
			}
		case tea.WindowSizeMsg:
			m.searchList.SetSize(msg.Width, msg.Height)
		}
		var cmd tea.Cmd
		m.searchList, cmd = m.searchList.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.Type {

		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyCtrlH: // Search
			if historyMgr != nil {
				m.inSearch = true
				// Pre-populate with recent sessions
				sessions, err := historyMgr.ListRecentSessions(20)
				if err == nil {
					items := []list.Item{}
					for _, s := range sessions {
						items = append(items, item{
							title: fmt.Sprintf("%s (%s)", s.Timestamp.Format("01/02 15:04"), s.Model),
							desc:  s.Summary,
							uuid:  s.UUID,
						})
					}
					m.searchList.SetItems(items)
				}
				// Size list
				m.searchList.SetSize(m.viewportWidth+2, m.viewport.Height+m.textarea.Height())
			}
			return m, nil

		case tea.KeyCtrlN: // ctrl+N
			m.llmMessages = []Message{}

			m.textarea.Reset()
			m.textarea.Placeholder = TEXTINPUT_PLACEHOLDER
			m.textarea.Focus()

			m.viewport.SetContent(`<llm chat history is empty>`)
			// m.viewport.SetContent(formatMessageLog(m.llmMessages))
			m.viewport.GotoBottom()

			return m, nil

		case tea.KeyShiftDown:
			m.shift = true
			return m, nil

		case tea.KeyShiftUp:
			m.shift = false
			return m, nil

		case tea.KeyCtrlS:
			if len(m.llmMessages) > 0 {
				putTextIntoClipboard(formatMessageLog(m.llmMessages, false, 0, 0, "", "", false, m.modelName))
			}
			return m, nil

		case tea.KeyCtrlE:
			if len(m.llmMessages) > 0 {
				putTextIntoClipboard(m.llmMessages[len(m.llmMessages)-1].Content)
			}
			return m, nil

		case tea.KeyCtrlD:
			removeLastMsg(m)

			m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, "", "", true, m.modelName))
			m.viewport.GotoBottom()

			return m, nil

		case tea.KeyEnter:
			if msg.Alt {
				m.textarea.SetValue(m.textarea.Value() + "\n")
			} else {
				var usermsg = m.textarea.Value()

				if len(strings.Trim(usermsg, " \r\t\n")) == 0 {
					return m, nil
				}

				ret, cmds := sendMsg(m, usermsg)

				return ret, tea.Batch(tiCmd, vpCmd, spCmd, cmds)
			}
		}

	case tea.WindowSizeMsg:
		m.textarea.SetWidth(msg.Width - 2)
		m.viewport.Width = msg.Width - 2
		m.viewportWidth = msg.Width - 2
		m.viewport.Height = msg.Height - 1 - m.textarea.Height()

	case updateViewportMsg:
		content := msg.content
		isReasoning := msg.isReasoning
		streaming_done := !msg.streaming

		// Handle Reasoning Start/Transition
		if isReasoning {
			if !m.isReasoning {
				m.isReasoning = true
				m.reasoningStart = time.Now()
				m.reasoningText = ""
			}
			m.reasoningText += content

			// Auto-scroll logic for viewport not needed as reasoning is external to viewport messages
		} else {
			// Content or Done
			if m.isReasoning {
				// Transition from reasoning to content
				m.isReasoning = false
				duration := time.Since(m.reasoningStart)
				durStr := fmt.Sprintf("%dm%ds", int(duration.Minutes()), int(duration.Seconds())%60)

				collapse := fmt.Sprintf("< thought for %s >\n\n", durStr)

				// Prepend to assistant message if it exists, or start new
				if len(m.llmMessages) > 0 && m.llmMessages[len(m.llmMessages)-1].Role == "assistant" {
					m.llmMessages[len(m.llmMessages)-1].Content += collapse
				} else {
					m.llmMessages = append(m.llmMessages, *NewMessage("assistant", collapse))
				}
				m.reasoningText = ""
			}
		}

		if m.spin {
			m.spin = false
			m.streaming = true
			m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
		}

		if streaming_done {
			m.streaming = false
			m.isReasoning = false // ensure closed
			return m, nil
		}

		if !isReasoning {
			if len(m.llmMessages) > 0 && m.llmMessages[len(m.llmMessages)-1].Role == "assistant" {
				m.llmMessages[len(m.llmMessages)-1].Content += content
			} else {
				m.llmMessages = append(m.llmMessages, *NewMessage("assistant", content))
				m.spin = false
			}
		}

		suffix := ""
		if m.isReasoning {
			// Render rolling reasoning log
			reasoningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#50C8FF"))
			barStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9D00FF"))
			width := m.viewportWidth
			if width <= 0 {
				width = 80
			}
			bar := strings.Repeat("-", width)
			suffix = fmt.Sprintf("\n%s\n%s\n%s", barStyle.Render(bar), reasoningStyle.Render(m.reasoningText), barStyle.Render(bar))
		}

		m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, suffix, "", true, m.modelName))
		m.viewport.GotoBottom()

		return m, tea.Batch(tiCmd, vpCmd, spCmd, readLLMResponse(m, m.ch))

	default:
		// fmt.Println(msg)
	}

	if m.spin || m.streaming {
		m.spinner, spCmd = m.spinner.Update(msg)
		return m, tea.Batch(tiCmd, vpCmd, spCmd)
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m chatTuiState) View() string {
	if m.inSearch {
		return m.searchList.View()
	}

	if m.spin || m.streaming {
		suffix := m.spinner.View()
		if m.isReasoning {
			reasoningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#50C8FF"))
			barStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9D00FF"))
			width := m.viewportWidth
			if width <= 0 {
				width = 80
			}
			bar := strings.Repeat("-", width)
			suffix = fmt.Sprintf("\n%s\n%s\n%s", barStyle.Render(bar), reasoningStyle.Render(m.reasoningText), barStyle.Render(bar))
		}
		m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, suffix, "", true, m.modelName))
	}

	return fmt.Sprintf(
		"%s\n%s",
		m.viewport.View(),
		m.textarea.View(),
	) + "\n"
}

func readLLMResponse(m chatTuiState, ch <-chan StreamEvent) tea.Cmd {
	return func() tea.Msg {
		for event := range ch {
			if event.Type == "content" {
				return updateViewportMsg{content: event.Content, streaming: true, isReasoning: false}
			} else if event.Type == "reasoning" {
				return updateViewportMsg{content: event.Content, streaming: true, isReasoning: true}
			}
		}
		var lastMsg, err = getLastMsg(m)
		if err == nil {
			m.historyApi(lastMsg)
		}
		return updateViewportMsg{content: "", streaming: false}
	}
}

type updateViewportMsg struct {
	streaming   bool
	content     string
	isReasoning bool
}
