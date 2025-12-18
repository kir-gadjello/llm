package history

import (
	"fmt"
	"regexp"
	"strings"
)

// ParseQuery converts user input into FTS5 syntax
// Supports: "phrase search", user:term, ai:term
func ParseQuery(input string) string {
	var parts []string

	// Normalize spaces
	input = strings.TrimSpace(input)

	// Handle field filters
	// This is a naive parser. For robust handling, we'd need a lexer.
	// We'll process space-separated tokens, respecting quotes.

	// Regex to split by space but keep quotes
	re := regexp.MustCompile(`[^\s"']+|"([^"]*)"|'([^']*)'`)
	tokens := re.FindAllString(input, -1)

	for _, token := range tokens {
		// Handle quotes
		if strings.HasPrefix(token, "\"") || strings.HasPrefix(token, "'") {
			// Pass through exact phrases
			parts = append(parts, token)
			continue
		}

		// Handle prefixes
		lower := strings.ToLower(token)
		if strings.HasPrefix(lower, "user:") {
			term := token[5:]
			if term != "" {
				parts = append(parts, fmt.Sprintf("(role:user AND content:%s)", term))
			} else {
				parts = append(parts, "role:user")
			}
		} else if strings.HasPrefix(lower, "ai:") || strings.HasPrefix(lower, "assistant:") {
			idx := strings.Index(token, ":")
			term := token[idx+1:]
			if term != "" {
				parts = append(parts, fmt.Sprintf("(role:assistant AND content:%s)", term))
			} else {
				parts = append(parts, "role:assistant")
			}
		} else if strings.HasPrefix(lower, "system:") {
			term := token[7:]
			if term != "" {
				parts = append(parts, fmt.Sprintf("(role:system AND content:%s)", term))
			} else {
				parts = append(parts, "role:system")
			}
		} else {
			// Standard match
			// Add * for prefix matching if it's a word
			if len(token) > 3 && regexp.MustCompile(`^[a-zA-Z0-9]+$`).MatchString(token) {
				parts = append(parts, token+"*")
			} else {
				parts = append(parts, token)
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, " AND ")
}
