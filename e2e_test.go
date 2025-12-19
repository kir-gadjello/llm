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

	cmd := exec.Command("go", "build", "-tags", "sqlite_fts5", "-o", llmBinaryPath, ".")
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
					// Special Logic: Auto Selector
					if strings.Contains(content, "smart file selector") {
						response := map[string]interface{}{
							"choices": []interface{}{
								map[string]interface{}{
									"message": map[string]interface{}{
										"role":    "assistant",
										"content": "subdir/test.txt",
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
	Name      string
	Args      []string // CLI args
	In        string   // Stdin
	Conf      string   // Optional .llmterm.yaml content
	Want      string   // Substring expected in output (which is the echoed request)
	WantMiss  string   // Substring expected to be MISSING from output
	ExpectErr bool     // Expect command to fail
}

func TestCLI(t *testing.T) {
	// 1. Setup Server
	server := httptest.NewServer(MockEchoHandler())
	defer server.Close()

	// 2. Setup Environment
	tempHome, _ := os.MkdirTemp("", "llm-home")
	defer os.RemoveAll(tempHome)
	tempCwd, _ := os.MkdirTemp("", "llm-cwd")
	if resolved, err := filepath.EvalSymlinks(tempCwd); err == nil {
		tempCwd = resolved
	}
	defer os.RemoveAll(tempCwd)

	// Create dummy file for file tests
	os.MkdirAll(filepath.Join(tempCwd, "subdir"), 0755)
	os.WriteFile(filepath.Join(tempCwd, "subdir", "test.txt"), []byte("file_content"), 0644)
	absPath := filepath.Join(tempCwd, "subdir", "test.txt")
	if runtime.GOOS == "windows" {
		absPath = strings.ReplaceAll(absPath, "\\", "\\\\")
	}

	// Initialize Git for @staged tests
	exec.Command("git", "-C", tempCwd, "init").Run()
	exec.Command("git", "-C", tempCwd, "config", "user.email", "test@example.com").Run()
	exec.Command("git", "-C", tempCwd, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", tempCwd, "add", ".").Run()

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
			Name:     "Temperature Omitted",
			Args:     []string{"hi"},
			Want:     `"model"`,
			WantMiss: `"temperature"`,
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
			Name:     "Max Tokens Omitted",
			Args:     []string{"hi"},
			Want:     `"model"`, // Ensure request was made
			WantMiss: `"max_tokens"`,
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
			Want: `"reasoning_effort":"low"`,
		},
		{
			Name: "Reasoning High",
			Args: []string{"--reasoning-high", "think hard"},
			Want: `"reasoning_effort":"high"`,
		},
		{
			Name: "Reasoning XHigh",
			Args: []string{"--reasoning-xhigh", "think super hard"},
			Want: `"reasoning_effort":"xhigh"`,
		},
		{
			Name: "Reasoning Custom",
			Args: []string{"--reasoning", "mega-brain", "think"},
			Want: `"reasoning_effort":"mega-brain"`,
		},
		{
			Name: "Verbosity High",
			Args: []string{"--verbosity", "high", "verbose mode"},
			Want: `"verbosity":"high"`,
		},
		{
			Name: "Reasoning Max Tokens",
			Args: []string{"-R", "5000", "think"},
			Want: `"reasoning":{"max_tokens":5000}`,
		},
		{
			Name: "Reasoning Exclude",
			Args: []string{"--reasoning-high", "--reasoning-exclude", "think"},
			// Expects flat reasoning_effort AND reasoning object with exclude
			Want: `"reasoning":{"exclude":true},"reasoning_effort":"high"`,
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
			// Armor is on by default, piped wrapper "context" is merged into armor
			Want: "\\u003ccontext\\u003e\\nsome_data\\n\\u003c/context\\u003e",
		},
		{
			Name: "Piped Input (Custom Wrapper)",
			In:   "some_data",
			Args: []string{"-w", "code", "analyze"},
			// Armor wraps custom wrapper
			Want: "\\u003ccontext\\u003e\\n\\u003ccode\\u003e\\nsome_data\\n\\u003c/code\\u003e\\n\\u003c/context\\u003e",
		},
		{
			Name: "Piped Input (No Wrapper)",
			In:   "raw_data",
			Args: []string{"-w", "", "analyze"},
			// Armor wraps raw data
			Want: "\\u003ccontext\\u003e\\nraw_data\\n\\u003c/context\\u003e",
		},
		{
			Name: "Context Order Append",
			In:   "data",
			Args: []string{"-w", "ctx", "--context-order", "append", "prompt"},
			// Armor wraps custom ctx
			Want: "prompt\\n\\n\\u003ccontext\\u003e\\n\\u003cctx\\u003e\\ndata\\n\\u003c/ctx\\u003e\\n\\u003c/context\\u003e",
		},
		{
			Name: "Context Armor Disabled",
			In:   "data",
			Args: []string{"-w", "ctx", "--context-armor=false", "prompt"},
			// No outer context tag
			Want: "\\u003cctx\\u003e\\ndata\\n\\u003c/ctx\\u003e",
		},

		// --- File Names ---
		{
			Name: "Show Filenames Relative",
			Args: []string{"-f", "subdir/test.txt", "--show-filenames", "relative", "hi"},
			Want: "File: subdir/test.txt",
		},
		{
			Name: "Show Filenames Absolute",
			Args: []string{"-f", "subdir/test.txt", "--show-filenames", "absolute", "hi"},
			Want: "File: " + absPath,
		},
		{
			Name: "Show Filenames NameOnly",
			Args: []string{"-f", "subdir/test.txt", "--show-filenames", "name-only", "hi"},
			Want: "File: test.txt",
		},
		{
			Name:     "Show Filenames None",
			Args:     []string{"-f", "subdir/test.txt", "--show-filenames", "none", "hi"},
			Want:     "```text\\nfile_content\\n```",
			WantMiss: "File: ",
		},

		// --- Context Format ---
		{
			Name: "Context XML (Armored)",
			Args: []string{"-f", "subdir/test.txt", "-i", "xml", "hi"},
			// formatContext should NOT output outer <context>, main should do it
			Want: "\\u003ccontext\\u003e\\n\\u003cfiles\\u003e\\n\\u003cfile path=\\\"subdir/test.txt\\\"\\u003e",
		},
		{
			Name: "Context XML (Unarmored)",
			Args: []string{"-f", "subdir/test.txt", "-i", "xml", "--context-armor=false", "hi"},
			// formatContext should output outer <context> for backwards compat
			Want: "\\u003ccontext\\u003e\\n\\u003cfiles\\u003e\\n\\u003cfile path=\\\"subdir/test.txt\\\"\\u003e",
		},

		// --- Path Expansion & Git ---
		{
			Name: "Glob Expansion",
			Args: []string{"-f", "subdir/*.txt", "hi"},
			Want: "File: subdir/test.txt",
		},
		{
			Name: "Git Staged",
			Args: []string{"@staged", "hi"},
			Want: "File: subdir/test.txt",
		},

		// --- Auto Selector ---
		{
			Name: "Auto Selector",
			Args: []string{"-A", "hi"},
			Want: "File: subdir/test.txt",
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
		// --- Fix Regressions ---
		{
			Name: "Env Var Precedence",
			Args: []string{"hi"},
			Want: `"content":"hi"`,
		},
		// --- History Search E2E ---
		// We can't easily test sqlite FTS in this E2E harness because it shells out to the binary.
		// We would need to ensure the binary uses a predictable history path and wait for indexing.
		// However, we can test that the search command doesn't crash on empty/missing history.
		{
			Name: "Search Empty",
			Args: []string{"search", "banana"},
			Want: "No matches found.",
		},
		{
			Name: "Seed History & Search",
			Args: []string{"remember_this_keyword"},
			Want: "remember_this_keyword", // Verify it ran
		},
		// We run these in sequence, so the previous test seeded the DB in tempHome
		{
			Name: "Search Found",
			Args: []string{"search", "remember_this_keyword"},
			Want: "remember_this_keyword", // Should appear in search results
		},
		{
			Name: "Search Filter",
			Args: []string{"search", "user:remember_this_keyword"},
			Want: "remember_this_keyword",
		},
		{
			Name:      "Resume Missing Args",
			Args:      []string{"resume"},
			Want:      "requires at least 1 arg",
			ExpectErr: true,
		},
		{
			Name: "Resume Session",
			Args: []string{"resume", "bad-uuid"},
			Want: "session not found",
			ExpectErr: true,
		},
		{
			Name: "History List",
			Args: []string{"history"},
			Want: "remember_this_keyword", // Should list the session from previous test
		},
		{
			Name: "Chat Send Short Flag",
			Args: []string{"-C", "hello", "--dry"},
			Want: `"content": "hello"`,
		},
		{
			Name: "Save To File",
			Args: []string{"save_test", "--save-to", "output.md"},
			Want: "Saved output to",
		},
		{
			Name: "Save To Dir",
			Args: []string{"save_test_dir", "--save-to", "."},
			Want: "Saved output to",
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
			cmd.Dir = tempCwd

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
			if err != nil && !tc.ExpectErr {
				t.Fatalf("Command failed: %v\nOutput: %s", err, output)
			}
			if err == nil && tc.ExpectErr {
				t.Fatalf("Command expected to fail but succeeded\nOutput: %s", output)
			}

			if !strings.Contains(output, tc.Want) {
				t.Errorf("Want substring %q not found in output.\n--- CLI Output (Echoed Request) ---\n%s\n-----------------------------------", tc.Want, output)
			}
			if tc.WantMiss != "" && strings.Contains(output, tc.WantMiss) {
				t.Errorf("WantMiss substring %q WAS found in output.\n--- CLI Output ---\n%s\n", tc.WantMiss, output)
			}
		})
	}
}
