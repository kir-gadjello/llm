package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var llmBinaryPath string

// TestMain builds the binary once
func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "llm-e2e-build")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	if runtime.GOOS == "windows" {
		llmBinaryPath = filepath.Join(tempDir, "llm.exe")
	} else {
		llmBinaryPath = filepath.Join(tempDir, "llm")
	}

	cmd := exec.Command("go", "build", "-o", llmBinaryPath, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Build failed: %v\nOutput:\n%s\n", err, output)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// MockEchoHandler echoes the received API request back as the "assistant response".
// This allows us to verify that CLI flags are correctly transformed into API parameters.
func MockEchoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)

		// decode to map to inspect arbitrary fields
		var reqMap map[string]interface{}
		json.Unmarshal(bodyBytes, &reqMap)

		// Special Logic: Shell Assistant
		// If the system prompt indicates shell assistant, return a valid code block
		// so the CLI logic (which parses markdown) succeeds.
		if messages, ok := reqMap["messages"].([]interface{}); ok {
			for _, m := range messages {
				msg := m.(map[string]interface{})
				if msg["role"] == "system" {
					content := msg["content"].(string)
					if strings.Contains(content, "generate a shell command") {
						response := map[string]interface{}{
							"choices": []interface{}{
								map[string]interface{}{
									"message": map[string]interface{}{
										"role":    "assistant",
										"content": "```bash\necho YOLO_SUCCESS\n```",
									},
								},
							},
						}
						json.NewEncoder(w).Encode(response)
						return
					}
				}
			}
		}

		// Default Logic: Return the Request JSON as the Response Content
		// This lets the test read stdout to see what was sent to the server.
		responseContent := string(bodyBytes)

		resp := map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": responseContent,
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// Case defines a simplified test scenario
type Case struct {
	Name string
	Args []string // CLI args
	In   string   // Stdin
	Conf string   // Optional .llmterm.yaml content
	Want string   // Substring expected in output (which is the echoed request)
}

func TestCLI(t *testing.T) {
	// 1. Setup Server
	server := httptest.NewServer(MockEchoHandler())
	defer server.Close()

	// 2. Setup Environment
	tempHome, _ := os.MkdirTemp("", "llm-home")
	defer os.RemoveAll(tempHome)

	baseConfig := fmt.Sprintf("models:\n  default:\n    api_base: %s\n", server.URL)

	// 3. Define Test Cases
	cases := []Case{
		// --- Basic Flags ---
		{
			Name: "Temperature & Seed",
			Args: []string{"-t", "1.5", "-r", "999", "hi"},
			Want: `"temperature":1.5`,
		},
		{
			Name: "System Prompt",
			Args: []string{"-p", "act like a pirate", "hi"},
			Want: `"content":"act like a pirate"`,
		},
		{
			Name: "Max Tokens",
			Args: []string{"-N", "123", "hi"},
			Want: `"max_tokens":123`,
		},
		{
			Name: "JSON Mode",
			Args: []string{"-j", "generate json"},
			Want: `"response_format":{"type":"json_object"}`,
		},
		{
			Name: "Extra Params",
			Args: []string{"-e", `{"top_p":0.9, "stop":["EOS"]}`, "hi"},
			Want: `"top_p":0.9`,
		},
		{
			Name: "Model Selection",
			Args: []string{"-m", "gpt-4-turbo", "hi"},
			Want: `"model":"gpt-4-turbo"`,
		},

		// --- Reasoning Flags (DeepSeek/OpenRouter style) ---
		{
			Name: "Reasoning Low",
			Args: []string{"--reasoning-low", "think"},
			Want: `"reasoning":{"effort":"low"}`,
		},
		{
			Name: "Reasoning High",
			Args: []string{"--reasoning-high", "think hard"},
			Want: `"reasoning":{"effort":"high"}`,
		},
		{
			Name: "Reasoning Max Tokens",
			Args: []string{"-R", "5000", "think"},
			Want: `"reasoning":{"max_tokens":5000}`,
		},
		{
			Name: "Reasoning Exclude",
			Args: []string{"--reasoning-high", "--reasoning-exclude", "think"},
			Want: `"reasoning":{"effort":"high","exclude":true}`,
		},

		// --- Advanced Features ---
		{
			Name: "JSON Schema",
			Args: []string{"-J", `{"type":"string"}`, "hi"},
			Want: `"json_schema":{"type":"string"}`,
		},
		{
			Name: "Stop Sequences",
			Args: []string{"-X", `["User:", "End"]`, "hi"},
			Want: `"stop":["User:","End"]`,
		},

		// --- Input Handling ---
		{
			Name: "Piped Input (Default)",
			In:   "some_data",
			Args: []string{"analyze"},
			Want: "\\u003ccontext\\u003e\\nsome_data\\n\\u003c/context\\u003e",
		},
		{
			Name: "Piped Input (Custom Wrapper)",
			In:   "some_data",
			Args: []string{"-w", "code", "analyze"},
			Want: "\\u003ccode\\u003e\\nsome_data\\n\\u003c/code\\u003e",
		},
		{
			Name: "Piped Input (No Wrapper)",
			In:   "raw_data",
			Args: []string{"-w", "", "analyze"},
			Want: "raw_data\\n\\nanalyze", // Appended raw
		},
		{
			Name: "Context Order Append",
			In:   "data",
			Args: []string{"-w", "ctx", "--context-order", "append", "prompt"},
			Want: "prompt\\n\\n\\u003cctx\\u003e\\ndata\\n\\u003c/ctx\\u003e",
		},

		// --- Config Files ---
		{
			Name: "Config Profile",
			Conf: `
models:
  my-pro:
    api_base: ` + server.URL + `
    temperature: 0.2
    extra_body:
      logit_bias: { "50256": -100 }
`,
			Args: []string{"-m", "my-pro", "hi"},
			Want: `"logit_bias":{"50256":-100}`,
		},

		// --- Shell Assistant (YOLO) ---
		{
			Name: "Shell Assistant Exec",
			Args: []string{"-s", "-y", "list files"},
			Want: "YOLO_SUCCESS",
		},
	}

	// 4. Execution Loop
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			// Write config
			configContent := baseConfig
			if tc.Conf != "" {
				configContent = tc.Conf
			}
			configFile := filepath.Join(tempHome, ".llmterm.yaml")
			os.WriteFile(configFile, []byte(configContent), 0644)

			// Prepare Command
			cmd := exec.Command(llmBinaryPath, tc.Args...)

			// Environment
			cmd.Env = append(os.Environ(),
				fmt.Sprintf("HOME=%s", tempHome),
				fmt.Sprintf("USERPROFILE=%s", tempHome), // Windows support
				"OPENAI_API_KEY=dummy",
				fmt.Sprintf("OPENAI_API_BASE=%s", server.URL),
				"TERM=dumb",
				"SHELL=/bin/sh",
			)

			// Stdin
			if tc.In != "" {
				cmd.Stdin = strings.NewReader(tc.In)
			}

			// Run
			outputBytes, err := cmd.CombinedOutput()
			output := string(outputBytes)

			// Assert
			if err != nil {
				t.Fatalf("Command failed: %v\nOutput: %s", err, output)
			}

			if !strings.Contains(output, tc.Want) {
				t.Errorf("Want substring %q not found in output.\n--- CLI Output (Echoed Request) ---\n%s\n-----------------------------------", tc.Want, output)
			}
		})
	}
}
