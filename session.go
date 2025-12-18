package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/kir-gadjello/llm/history"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// RingBuffer is a fixed-size circular buffer for byte data
type RingBuffer struct {
	data []byte
	size int
	pos  int
	full bool
	mu   sync.RWMutex
}

func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		data: make([]byte, size),
		size: size,
	}
}

func (r *RingBuffer) Write(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n = len(p)
	if n > r.size {
		// If writing more than size, just take the last size bytes
		p = p[n-r.size:]
		r.pos = 0
		r.full = true
		copy(r.data, p)
		return n, nil
	}

	remaining := r.size - r.pos
	if n <= remaining {
		copy(r.data[r.pos:], p)
		r.pos += n
	} else {
		copy(r.data[r.pos:], p[:remaining])
		copy(r.data[0:], p[remaining:])
		r.pos = n - remaining
		r.full = true
	}
	return n, nil
}

func (r *RingBuffer) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.full {
		return string(r.data[:r.pos])
	}

	// Reconstruct in order
	out := make([]byte, r.size)
	copy(out, r.data[r.pos:])
	copy(out[r.size-r.pos:], r.data[:r.pos])
	return string(out)
}

// --- Structured History Types ---

type CommandEvent struct {
	Command   string
	Output    string
	ExitCode  int
	Timestamp time.Time
}

type SessionHistory struct {
	Events []CommandEvent
	// Buffer for the current incomplete event
	currentOutput strings.Builder
	currentInput  strings.Builder
	mu            sync.RWMutex
}

func NewSessionHistory() *SessionHistory {
	return &SessionHistory{
		Events: make([]CommandEvent, 0),
	}
}

func (h *SessionHistory) AddEvent(cmd string, output string, exitCode int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Events = append(h.Events, CommandEvent{
		Command:   cmd,
		Output:    output,
		ExitCode:  exitCode,
		Timestamp: time.Now(),
	})
}

func (h *SessionHistory) GetLastEvents(n int) []CommandEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.Events) == 0 {
		return nil
	}
	if n > len(h.Events) {
		return h.Events
	}
	return h.Events[len(h.Events)-n:]
}

// --- OSC 133 Parser ---

// OSC 133 Constants
const (
	OSC_START = "\x1b]133;"
	OSC_END   = "\x07"
)

// ParserState tracks where we are in the shell lifecycle
type ParserState int

const (
	StateNone ParserState = iota
	StatePrompt
	StateInput
	StateOutput
)

type SessionParser struct {
	history *SessionHistory
	state   ParserState
	buf     strings.Builder // Accumulates raw output for the current state
}

func NewSessionParser(hist *SessionHistory) *SessionParser {
	return &SessionParser{
		history: hist,
		state:   StateNone,
	}
}

// ParseChunk processes a chunk of PTY output and updates history
// This is a simplified parser that looks for OSC sequences.
// A full VT100 parser is complex; we'll use regex/string searching for the specific OSC 133 codes.
// Note: This assumes OSC codes are not split across chunks (mostly true for small chunks).
func (p *SessionParser) ParseChunk(chunk []byte) {
	s := string(chunk)

	// We need to handle the case where we are accumulating data for the current state
	// AND watching for state transitions.

	// Simple state machine approach:
	// 1. Split by OSC start sequence
	parts := strings.Split(s, "\x1b]133;")

	if len(parts) == 1 {
		// No OSC sequence found, just append to current state buffer
		p.appendToCurrentState(s)
		return
	}

	// Handle first part (belongs to previous state)
	p.appendToCurrentState(parts[0])

	// Handle subsequent parts (start with a code like "A" or "C")
	for _, part := range parts[1:] {
		// Find the terminator (BEL \x07 or ST \x1b\\)
		// We only support BEL \x07 for simplicity as per our scripts
		endIdx := strings.Index(part, "\x07")
		if endIdx == -1 {
			// Incomplete sequence? In a real parser we'd buffer this.
			// For now, treat as raw text to avoid data loss, though it might show garbage.
			p.appendToCurrentState("\x1b]133;" + part)
			continue
		}

		codeAndArgs := part[:endIdx]
		content := part[endIdx+1:]

		// Process the transition
		p.handleTransition(codeAndArgs)

		// Append the rest as content for the NEW state
		p.appendToCurrentState(content)
	}
}

func (p *SessionParser) appendToCurrentState(s string) {
	switch p.state {
	case StateOutput:
		p.history.currentOutput.WriteString(s)
	case StateInput: // Actually usually "Pre-Output" or "Prompt-End"
		// In many shells, input is echoed back, so it appears in the stream.
		// We can capture it here.
		p.history.currentInput.WriteString(s)
	case StatePrompt:
		// We ignore prompt text for the history (noise), but could capture it if needed.
	}
}

func (p *SessionParser) handleTransition(code string) {
	// code is like "A", "C", "D;0"
	if code == "A" {
		// Start of Prompt
		p.state = StatePrompt
	} else if code == "C" {
		// Start of Output (End of Command Input)
		// Finalize input capture
		// But wait, where is the command?
		// In a PTY, the command is echoed characters.
		// So 'StateInput' buffer contains the command.
		p.state = StateOutput
		p.history.currentOutput.Reset()
	} else if strings.HasPrefix(code, "D") {
		// End of Command (D;exitcode)
		// Finalize the event
		parts := strings.Split(code, ";")
		exitCode := 0
		if len(parts) > 1 {
			fmt.Sscanf(parts[1], "%d", &exitCode)
		}

		cmd := strings.TrimSpace(p.history.currentInput.String())
		out := cleanTerminalOutput(p.history.currentOutput.String())

		// Only add if we have something (avoid empty prompts)
		if cmd != "" || out != "" {
			p.history.AddEvent(cmd, out, exitCode)
		}

		// Reset buffers
		p.history.currentInput.Reset()
		p.history.currentOutput.Reset()
		p.state = StateNone
	} else if code == "B" {
		// End of Prompt (Start of Input)
		p.state = StateInput
		p.history.currentInput.Reset()
	}
}

// cleanTerminalOutput removes ANSI escape codes for better LLM context
func cleanTerminalOutput(input string) string {
	// Regex to remove standard ANSI escape codes
	ansi := regexp.MustCompile(`\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)
	return ansi.ReplaceAllString(input, "")
}

func runSession(cmd *cobra.Command, args []string, cfg *ConfigFile) error {
	// 1. Detect Shell
	shellInfo := detectShell()
	if shellInfo.Path == "" {
		return fmt.Errorf("could not detect shell")
	}

	fmt.Printf("Starting llm session in %s...\n", shellInfo.Path)
	fmt.Println("Type '?? your question' to ask the LLM. Type 'exit' to quit.")

	// Tip for integration
	fmt.Printf("\033[1;33mTip: Run 'source <(llm integration %s)' for better context awareness.\033[0m\n", shellInfo.Name)

	time.Sleep(1 * time.Second)

	// 2. Start PTY
	c := exec.Command(shellInfo.Path)
	ptmx, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// 3. Handle Window Resize
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				log.Printf("error resizing pty: %s", err)
			}
		}
	}()
	ch <- syscall.SIGWINCH // Initial resize

	// 4. Put Terminal in Raw Mode
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	// 5. Output Capture (Structured + Raw Fallback)
	// We keep the raw ring buffer as a fallback if no OSC codes are seen
	rawHistoryBuf := NewRingBuffer(64 * 1024)

	// Structured History
	structHistory := NewSessionHistory()
	parser := NewSessionParser(structHistory)

	// MultiWriter: PTY Output -> Stdout (User) + RingBuffer + Parser
	// We need a custom writer for the parser to avoid locking issues or blocking
	parserWriter := &ParserWriter{parser: parser}

	go func() {
		mw := io.MultiWriter(os.Stdout, rawHistoryBuf, parserWriter)
		_, _ = io.Copy(mw, ptmx)
	}()

	// 6. Input Interception Loop
	// We need to read Stdin byte by byte to detect '??'
	// We run this in a goroutine so we don't block the main thread from exiting when the shell dies.
	go func() {
		inputBuf := make([]byte, 1024) // Current line buffer
		inputIdx := 0
		readBuf := make([]byte, 1)

		for {
			n, err := os.Stdin.Read(readBuf)
			if err != nil {
				// If Stdin closes, we can't read anymore.
				return
			}
			if n == 0 {
				continue
			}

			b := readBuf[0]

			// Handle Input Buffer Logic
			if b == '\r' || b == '\n' {
				// Enter pressed
				line := string(inputBuf[:inputIdx])

				if strings.HasPrefix(line, "??") || strings.HasPrefix(line, " ??") {
					// === INTERCEPTION TRIGGERED ===

					// 1. Cancel the command in the shell
					_, _ = ptmx.Write([]byte{21}) // Ctrl+U
					fmt.Print("\r\n")

					// 2. Prepare Context
					// Check if we have structured events
					var context string
					lastEvents := structHistory.GetLastEvents(5)

					if len(lastEvents) > 0 {
						// Use structured history
						var sb strings.Builder
						sb.WriteString("Session History (Structured):\n")
						for _, e := range lastEvents {
							sb.WriteString(fmt.Sprintf("Command: %s\nExit Code: %d\nOutput:\n%s\n---\n", e.Command, e.ExitCode, e.Output))
						}
						context = sb.String()
					} else {
						// Fallback to raw history
						raw := rawHistoryBuf.String()
						context = cleanTerminalOutput(raw)
					}

					query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "??"))

					// 3. Run LLM
					runInlineLLM(cmd, cfg, query, context)

					// 4. Reset Buffer
					inputIdx = 0
					// 5. Force a prompt redraw
					_, _ = ptmx.Write([]byte{'\r'})

				} else {
					// Standard Enter: Pass through and reset buffer
					_, _ = ptmx.Write([]byte{b})
					inputIdx = 0
				}
			} else if b == 127 || b == 8 {
				// Backspace
				if inputIdx > 0 {
					inputIdx--
				}
				_, _ = ptmx.Write([]byte{b})
			} else if b == 3 {
				// Ctrl+C
				inputIdx = 0
				_, _ = ptmx.Write([]byte{b})
			} else if b == 4 {
				// Ctrl+D (EOF)
				// If line is empty, this usually means exit.
				// Pass it to PTY.
				_, _ = ptmx.Write([]byte{b})
				// We don't exit here explicitly; we let the PTY exit, which triggers c.Wait()
			} else {
				// Normal char
				if inputIdx < len(inputBuf) {
					inputBuf[inputIdx] = b
					inputIdx++
				}
				_, _ = ptmx.Write([]byte{b})
			}
		}
	}()

	// Wait for the shell process to exit
	if err := c.Wait(); err != nil {
		// It's normal for shells to exit with status != 0 sometimes, or if killed.
		// We just log it if it's interesting, but usually we just return.
		// fmt.Printf("Shell exited: %v\r\n", err)
	}

	return nil
}

// ParserWriter adapts the Write interface for the parser
type ParserWriter struct {
	parser *SessionParser
}

func (w *ParserWriter) Write(p []byte) (n int, err error) {
	// We make a copy to avoid race conditions if the parser stores the slice
	// (though our parser currently uses strings, so it copies anyway)
	w.parser.ParseChunk(p)
	return len(p), nil
}

func runInlineLLM(cmd *cobra.Command, cfg *ConfigFile, query string, historyStr string) {
	// 4. Extract flags (moved up to get model name)
	modelname, _ := cmd.Flags().GetString("model")
	if modelname == "" {
		if cfg.Default != "" {
			modelname = cfg.Default
		} else {
			modelname = getFirstEnv("gpt-4o-mini", "gpt-3.5-turbo", "OPENAI_API_MODEL", "GROQ_API_MODEL")
		}
	}

	// Resolve configuration
	runCfg, err := getRunConfig(cmd, cfg, modelname)
	if err != nil {
		fmt.Printf("\r\nError resolving config: %v\r\n", err)
		return
	}

	// Apply resolved config
	modelname = runCfg.ModelName
	apiKey := runCfg.ApiKey
	apiBase := runCfg.ApiBase
	temperature := runCfg.Temperature
	seed := runCfg.Seed
	maxTokens := runCfg.MaxTokens
	timeout := runCfg.Timeout

	// 1. Visual separator
	fmt.Printf("\r\033[1;34m🤖 %s:\033[0m\r\n", modelname)

	// 2. Prepare Prompt
	systemPrompt := "You are a helpful CLI assistant. The user is asking a question about their current terminal session. " +
		"Use the provided terminal history context to answer. Be concise. Output markdown."

	// 3. Construct message
	fullContext := fmt.Sprintf("<terminal-history>\n%s\n</terminal-history>\n\nUser Question: %s", historyStr, query)

	messages := []LLMMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: fullContext},
	}

	// === FIX: Handle Debug (-D) & Verbose (-v) ===
	verbose, _ := cmd.Flags().GetBool("verbose")
	debug, _ := cmd.Flags().GetBool("debug")

	if debug {
		// detailed prompt dump, formatted for Raw Mode (\r\n)
		fmt.Printf("\r\n\033[1;30m[DEBUG] Model: %s\033[0m\r\n", modelname)
		fmt.Printf("\033[1;30m[DEBUG] System Prompt:\033[0m\r\n%s\r\n", strings.ReplaceAll(systemPrompt, "\n", "\r\n"))
		fmt.Printf("\033[1;30m[DEBUG] Context:\033[0m\r\n%s\r\n", strings.ReplaceAll(fullContext, "\n", "\r\n"))
		fmt.Print("\r\n")
	}

	extra := map[string]interface{}{}
	if maxTokens > 0 {
		extra["max_tokens"] = maxTokens
	}

	// Merge ExtraBody
	for k, v := range runCfg.ExtraBody {
		extra[k] = v
	}

	// Reasoning Logic
	var reasoningObj map[string]interface{}
	if runCfg.ReasoningEffort != "" && runCfg.ReasoningEffort != "none" {
		reasoningObj = map[string]interface{}{
			"effort": runCfg.ReasoningEffort,
		}
	} else if runCfg.ReasoningMaxTokens > 0 {
		reasoningObj = map[string]interface{}{
			"max_tokens": runCfg.ReasoningMaxTokens,
		}
	}

	if runCfg.ReasoningExclude && reasoningObj != nil {
		reasoningObj["exclude"] = true
	}

	if reasoningObj != nil {
		extra["reasoning"] = reasoningObj
	}

	// 5. Call LLM
	// Note: llmChat handles 'verbose' (HTTP logs), but those logs write to stdout/stderr.
	// In Raw Mode, standard log/fmt might step. Ideally, we'd wrap the writer, but for -v
	// it's acceptable if it's slightly messy since it's for debugging.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ch, err := llmChat(ctx, messages, modelname, seed, temperature, nil, apiKey, apiBase, true, extra, verbose)
	if err != nil {
		fmt.Printf("\r\nError: %v\r\n", err)
		return
	}

	// 6. Stream output with \r\n fix for Raw Mode
	for event := range ch {
		if event.Type == "content" {
			// Raw mode requires \r\n for newlines, otherwise cursor just drops down without moving left
			chunk := strings.ReplaceAll(event.Content, "\n", "\r\n")
			fmt.Print(chunk)
		}
	}
	fmt.Print("\r\n")

	// 7. Log to history (optional)
	if historyMgr != nil {
		data := history.ShellEvent{
			Type:    "session_interception",
			Query:   query,
			History: historyStr,
		}
		historyMgr.SaveShellEvent(data)
	}
}
