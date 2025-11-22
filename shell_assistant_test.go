package main

import "testing"

func TestShellAssistant_ExtractCommand(t *testing.T) {
	tests := []struct {
		name     string
		response string
		expected string
	}{
		{
			name:     "Standard Markdown Block",
			response: "Here is the command:\n```bash\nfind . -name '*.go'\n```",
			expected: "find . -name '*.go'",
		},
		{
			name:     "Plain Text (No Markdown)",
			response: "ls -la",
			expected: "ls -la",
		},
		{
			name:     "Generic Code Block",
			response: "```\ngrep -r 'TODO' .\n```",
			expected: "grep -r 'TODO' .",
		},
		{
			name:     "Multi-line Command",
			response: "```bash\ndocker run -d \\\n  -p 8080:80 \\\n  nginx\n```",
			expected: "docker run -d \\\n  -p 8080:80 \\\n  nginx",
		},
		{
			name:     "Chatty Response",
			response: "Sure! You can use `sed` for this.\n\n```bash\nsed -i 's/foo/bar/g' file.txt\n```\n\nMake sure to back up your file first.",
			expected: "sed -i 's/foo/bar/g' file.txt",
		},
		{
			name:     "Multiple Blocks (Take First)",
			response: "Option 1:\n```bash\necho one\n```\nOption 2:\n```bash\necho two\n```",
			expected: "echo one",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractCommand(tc.response)
			if result != tc.expected {
				t.Errorf("Expected:\n%q\nGot:\n%q", tc.expected, result)
			}
		})
	}
}