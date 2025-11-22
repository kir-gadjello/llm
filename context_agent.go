package main

import (
	"fmt"
	"os"
	"strings"
)

// AutoSelector uses an LLM to select relevant files based on a query
type AutoSelector struct {
	verbose bool
}

// NewAutoSelector creates a new auto selector
func NewAutoSelector(verbose bool) *AutoSelector {
	return &AutoSelector{verbose: verbose}
}

// SelectFiles calls an LLM to select relevant files from a repo map
func (as *AutoSelector) SelectFiles(
	query string,
	repoMap string,
	selectorModel string,
	apiKey string,
	apiBase string,
	debug bool,
) ([]string, error) {
	// Build system prompt
	systemPrompt := `You are a smart file selector for a code analysis tool. Given a repository map and a user query, you must return ONLY newline-separated file paths that are likely relevant to the query.

Rules:
- Output MUST be a list (newline-separeted) of valid paths array:
path/to/file1.go
path/to/file2.py
...
- Select as little or as many relevant files as you see from the user query and repo map.
- Prefer files that directly implement or test the query subject
- Include related types, interfaces, or base classes if relevant
- Do NOT include any explanatory text, ONLY the list of paths
- If no files are relevant, return an empty list`

	// Build user prompt
	userPrompt := fmt.Sprintf(`<repomap>
%s
</repomap>

Query: %s

Return the list of relevant file paths:`, repoMap, query)

	if debug {
		fmt.Printf("AUTO-SELECTOR PROMPT:\n%s\n\nUSER PROMPT:\n%s\n", systemPrompt, userPrompt)
	}

	// Create messages
	messages := []LLMMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Call LLM (non-streaming)
	ch, err := llmChat(
		messages,
		selectorModel,
		42,  // seed
		0.0, // temperature (deterministic)
		nil, // no postprocess
		apiKey,
		apiBase,
		false, // no streaming
		nil,   // no extra params
		as.verbose,
	)

	if err != nil {
		if as.verbose {
			fmt.Fprintf(os.Stderr, "Auto-selector LLM call failed: %v\n", err)
		}
		return []string{}, nil // fail gracefully
	}

	// Read response
	var response string
	for chunk := range ch {
		response += chunk
	}

	response = strings.TrimSpace(response)

	if as.verbose {
		fmt.Fprintf(os.Stderr, "Auto-selector response: %s\n", response)
	}

	// Parse newline-separated paths
	var paths []string
	if response != "" {
		lines := strings.Split(response, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			// Skip empty lines
			if line == "" {
				continue
			}
			paths = append(paths, line)
		}
	}

	if as.verbose {
		fmt.Fprintf(os.Stderr, "Auto-selector selected %d files: %v\n", len(paths), paths)
	}

	return paths, nil
}
