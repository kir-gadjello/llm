package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func runShellAssistant(cmd *cobra.Command, args []string, cfg *ConfigFile) error {
	userRequest := strings.Join(args, " ")
	if userRequest == "" {
		return fmt.Errorf("please provide a description of the command you want to execute")
	}

	shellInfo := detectShell()
	envContext := getEnvironmentContext(shellInfo)

	systemPrompt := fmt.Sprintf(`You are an advanced terminal shell assistant.
Your task is to generate a shell command based on the user's request.
You should output ONLY the command in a markdown code block.
Do not output any other text.

Environment Context:
%s
`, envContext)

	// Get model configuration
	modelname, _ := cmd.Flags().GetString("model")
	if modelname == "" {
		if cfg.Default != "" {
			modelname = cfg.Default
		} else {
			modelname = getFirstEnv("gpt-3.5-turbo", "OPENAI_API_MODEL", "GROQ_API_MODEL", "LLM_MODEL")
		}
	}

	// Resolve configuration to handle model aliases
	runCfg, err := getRunConfig(cmd, cfg, modelname)
	if err != nil {
		log.Printf("Warning: failed to resolve config: %v", err)
	}

	// Apply resolved config
	modelname = runCfg.ModelName
	apiKey := runCfg.ApiKey
	apiBase := runCfg.ApiBase
	temperature := runCfg.Temperature
	seed := runCfg.Seed
	maxTokens := runCfg.MaxTokens

	// We need to construct the messages
	messages := []LLMMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userRequest},
	}

	// For shell assistant, we probably don't want streaming for the command generation itself if we want to parse it easily,
	// but the user might want to see it being generated.
	// However, the requirement says "parse the llm output and find first markdown block".
	// So we should probably not stream, or stream into a buffer.
	// Let's disable streaming for the generation part to simplify parsing.
	verbose, _ := cmd.Flags().GetBool("verbose")
	debug, _ := cmd.Flags().GetBool("debug")

	var timings Timings
	if debug {
		fmt.Printf("System Prompt:\n%s\n\nUser Request:\n%s\n", systemPrompt, userRequest)
		timings.BinaryStartup = time.Since(startTime)
	}

	// Call LLM
	llmCallStartTime := time.Now()
	extra := map[string]interface{}{
		"max_tokens": maxTokens,
	}
	// Merge ExtraBody from config
	for k, v := range runCfg.ExtraBody {
		extra[k] = v
	}
	ch, err := llmChat(messages, modelname, seed, temperature, nil, apiKey, apiBase, false, extra, verbose)
	if err != nil {
		return err
	}

	if debug {
		timings.TimeToFirstLLMCall = llmCallStartTime.Sub(startTime)
	}

	var fullResponse strings.Builder
	firstChunk := true
	for chunk := range ch {
		if debug && firstChunk {
			timings.TimeToFirstChunk = time.Since(startTime)
			firstChunk = false
		}
		fullResponse.WriteString(chunk)
	}

	if debug {
		timings.TimeToComplete = time.Since(startTime)
		displayTimings(timings)
	}

	generatedCommand := extractCommand(fullResponse.String())
	if generatedCommand == "" {
		return fmt.Errorf("no command found in LLM response: %s", fullResponse.String())
	}

	yolo, _ := cmd.Flags().GetBool("yolo")
	if !cmd.Flags().Changed("yolo") && cfg.Shell != nil && cfg.Shell.Yolo != nil {
		yolo = *cfg.Shell.Yolo
	}

	if yolo {
		return executeShellCommand(shellInfo, generatedCommand)
	}

	return interactiveShellMenu(shellInfo, generatedCommand, userRequest, cmd, cfg)
}

func extractCommand(response string) string {
	// Simple markdown block extractor
	// Look for ```bash, ```sh, or just ```
	lines := strings.Split(response, "\n")
	var commandLines []string
	inBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inBlock {
				// End of block
				break
			} else {
				inBlock = true
				continue
			}
		}
		if inBlock {
			commandLines = append(commandLines, line)
		}
	}

	// If no blocks found, maybe the whole response is the command?
	if len(commandLines) == 0 {
		// Heuristic: if response is short and looks like code, use it.
		// For now, let's just return the trimmed response if no block found,
		return strings.Trim(strings.TrimSpace(response), "`")
	}

	return strings.TrimSpace(strings.Join(commandLines, "\n"))
}

func executeShellCommand(shell ShellInfo, command string) error {
	fmt.Printf("Executing: %s\n", command)

	// Use the shell to execute
	cmd := exec.Command(shell.Cmd, shell.Arg, command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		return err
	}

	// TODO: Add to history if successful?
	// The Rust impl does: if code == 0 && config.read().save_shell_history
	// We can implement that later.
	return nil
}

func interactiveShellMenu(shell ShellInfo, command string, originalRequest string, cmd *cobra.Command, cfg *ConfigFile) error {
	// Colors
	cmdColor := lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // Orange
	keyColor := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))  // Cyan
	dimColor := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // Dimmed

	for {
		fmt.Println(cmdColor.Render(command))

		options := []string{"execute", "revise", "describe", "copy", "quit"}
		var promptParts []string
		for _, opt := range options {
			firstChar := opt[:1]
			rest := opt[1:]
			promptParts = append(promptParts, fmt.Sprintf("%s%s", keyColor.Render(firstChar), rest))
		}
		prompt := strings.Join(promptParts, dimColor.Render(" | "))
		fmt.Printf("%s: ", prompt)

		// Read single key
		key, err := readSingleKey()
		if err != nil {
			return err
		}

		// Clear the menu line if not quitting (or even if quitting, to be clean)
		// \r moves cursor to start of line, \033[K clears line
		if key != 'q' {
			fmt.Print("\r\033[K")
		} else {
			fmt.Println() // Keep the newline for quit
		}

		switch key {
		case 'e', '\r', '\n':
			return executeShellCommand(shell, command)
		case 'r':
			fmt.Print("Enter your revision: ")
			reader := bufio.NewReader(os.Stdin)
			revision, _ := reader.ReadString('\n')
			revision = strings.TrimSpace(revision)

			newRequest := fmt.Sprintf("%s\nRevision: %s", originalRequest, revision)

			return runShellAssistant(cmd, []string{newRequest}, cfg)

		case 'd':
			// Describe
			// We need to ask the LLM to explain the command.
			explainPrompt := fmt.Sprintf("Explain the following shell command briefly:\n\n%s", command)
			messages := []LLMMessage{
				{Role: "user", Content: explainPrompt},
			}

			// Resolve model configuration
			modelname, _ := cmd.Flags().GetString("model")
			if modelname == "" {
				if cfg.Default != "" {
					modelname = cfg.Default
				}
			}

			runCfg, err := getRunConfig(cmd, cfg, modelname)
			if err != nil {
				fmt.Printf("Warning: failed to resolve config: %v\n", err)
			}

			// Apply resolved config
			modelname = runCfg.ModelName
			apiKey := runCfg.ApiKey
			apiBase := runCfg.ApiBase

			extra := map[string]interface{}{}
			for k, v := range runCfg.ExtraBody {
				extra[k] = v
			}

			fmt.Println(dimColor.Render("Explanation:"))
			ch, err := llmChat(messages, modelname, 0, 0.7, nil, apiKey, apiBase, true, extra, false)
			if err != nil {
				fmt.Printf("Error getting description: %v\n", err)
				continue
			}

			for chunk := range ch {
				fmt.Print(chunk)
			}
			fmt.Println()
			fmt.Println()
			continue // Show menu again

		case 'c':
			err := putTextIntoClipboard(command)
			if err != nil {
				fmt.Printf("Error copying to clipboard: %v\n", err)
			} else {
				fmt.Println(dimColor.Render("✓ Copied the command."))
			}
			return nil // Exit after copy? Or stay? Rust impl breaks loop (exits).

		case 'q':
			return nil
		}
	}
}

func readSingleKey() (rune, error) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return 0, err
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	b := make([]byte, 1)
	_, err = os.Stdin.Read(b)
	if err != nil {
		return 0, err
	}
	return rune(b[0]), nil
}
