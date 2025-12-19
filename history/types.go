package history

import "time"

// SessionStartEvent represents the JSON structure logged when a chat starts
type SessionStartEvent struct {
	SID                string      `json:"sid"`
	TS                 int64       `json:"ts"`
	UserMsg            string      `json:"user_msg"`
	SystemPrompt       string      `json:"system_prompt"`
	Model              string      `json:"model"`
	Seed               int         `json:"seed"`
	Temperature        *float64    `json:"temperature,omitempty"`
	APIBase            string      `json:"api_base"`
	MaxTokens          int         `json:"max_tokens"`
	JSONMode           bool        `json:"json_mode"`
	StopSequences      interface{} `json:"stop_sequences"`
	ExtraParams        string      `json:"extra_params"`
	JsonSchema         string      `json:"json_schema"`
	ReasoningEffort    string      `json:"reasoning_effort,omitempty"`
	ReasoningMaxTokens int         `json:"reasoning_max_tokens,omitempty"`
	ReasoningExclude   bool        `json:"reasoning_exclude,omitempty"`
}

// MessageEvent represents the JSON structure logged for each message
type MessageEvent struct {
	ID      string      `json:"uuid"`
	SID     string      `json:"sid"`
	TS      int64       `json:"ts"`
	Message ChatMessage `json:"msg"`
}

// ChatMessage matches the internal Message struct
type ChatMessage struct {
	UUID    string   `json:"uuid"`
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

// ShellEvent represents session interception events
type ShellEvent struct {
	Type    string `json:"type"`
	Query   string `json:"query"`
	History string `json:"history_snippet"`
}

// SearchResult represents a hit from the FTS index
type SearchResult struct {
	SessionUUID string
	Timestamp   time.Time
	Role        string
	Content     string
	Preview     string
}

// SessionSummary represents a resume-able session
type SessionSummary struct {
	UUID      string
	Timestamp time.Time
	Summary   string
	Model     string
}
