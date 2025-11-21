package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type ShellInfo struct {
	Name       string // bash, zsh, fish, nushell, powershell, sh
	Path       string // full path to shell executable
	Cmd        string // command to execute shell
	Arg        string // argument flag for running commands (-c for most shells)
	HistoryCmd string // command to add to history (shell-specific)
}

// detectShell detects the current shell from environment variables and parent process
func detectShell() ShellInfo {
	// Try $SHELL first
	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		// Fallback: try to detect from parent process
		shellPath = detectParentShell()
	}

	if shellPath == "" {
		// Final fallback based on OS
		if runtime.GOOS == "windows" {
			shellPath = "powershell"
		} else {
			shellPath = "/bin/sh"
		}
	}

	shellName := filepath.Base(shellPath)
	shellName = strings.TrimSuffix(shellName, ".exe")

	// Determine shell-specific settings
	info := ShellInfo{
		Name: shellName,
		Path: shellPath,
		Cmd:  shellPath,
		Arg:  "-c",
	}

	// Shell-specific configurations
	switch {
	case strings.Contains(shellName, "zsh"):
		info.Name = "zsh"
	case strings.Contains(shellName, "bash"):
		info.Name = "bash"
	case strings.Contains(shellName, "fish"):
		info.Name = "fish"
		info.Arg = "-c"
	case strings.Contains(shellName, "nu"):
		info.Name = "nushell"
	case strings.Contains(shellName, "pwsh") || strings.Contains(shellName, "powershell"):
		info.Name = "powershell"
		info.Arg = "-Command"
	default:
		info.Name = "sh"
	}

	return info
}

// detectParentShell tries to detect shell from parent process
func detectParentShell() string {
	// Try ppid approach on Unix-like systems
	if runtime.GOOS != "windows" {
		cmd := exec.Command("ps", "-p", fmt.Sprintf("%d", os.Getppid()), "-o", "comm=")
		output, err := cmd.Output()
		if err == nil {
			shellName := strings.TrimSpace(string(output))
			if shellName != "" {
				// Try to find full path
				fullPath, err := exec.LookPath(shellName)
				if err == nil {
					return fullPath
				}
				return shellName
			}
		}
	}
	return ""
}

// getEnvironmentContext gathers system/shell context for the LLM prompt
func getEnvironmentContext(shell ShellInfo) string {
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "unknown"
	}

	osName := runtime.GOOS
	osDisplay := osName
	switch osName {
	case "darwin":
		osDisplay = "darwin (macOS)"
	case "linux":
		osDisplay = "linux"
	case "windows":
		osDisplay = "windows"
	}

	return fmt.Sprintf(`Shell: %s
OS: %s
User: %s
Directory: %s
Time: %s`,
		shell.Name,
		osDisplay,
		user,
		cwd,
		time.Now().Format(time.RFC1123))
}

// appendToShellHistory appends a command to the shell's history file
func appendToShellHistory(shell ShellInfo, command string, exitCode int, maxHistory int) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	var historyPath string
	var historyEntry string

	switch shell.Name {
	case "zsh":
		historyPath = filepath.Join(home, ".zsh_history")
		// Zsh format: : timestamp:0;command
		timestamp := time.Now().Unix()
		historyEntry = fmt.Sprintf(": %d:0;%s\n", timestamp, command)

	case "bash":
		historyPath = filepath.Join(home, ".bash_history")
		historyEntry = command + "\n"

	case "fish":
		historyPath = filepath.Join(home, ".local/share/fish/fish_history")
		// Fish format is YAML-like
		timestamp := time.Now().Unix()
		historyEntry = fmt.Sprintf("- cmd: %s\n  when: %d\n", command, timestamp)

	default:
		// Don't append to history for unknown shells
		return nil
	}

	// Trim history to max entries if specified
	if maxHistory > 0 {
		if err := trimHistoryFile(historyPath, maxHistory); err != nil {
			// Log but don't fail
			fmt.Fprintf(os.Stderr, "Warning: failed to trim history: %v\n", err)
		}
	}

	// Append to history file
	f, err := os.OpenFile(historyPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(historyEntry)
	return err
}

// trimHistoryFile keeps only the last N entries in a history file
func trimHistoryFile(path string, maxEntries int) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist yet
		}
		return err
	}

	lines := strings.Split(string(content), "\n")

	// Simple line-based trimming (works for bash, zsh)
	// Fish history is more complex but this is a reasonable approximation
	if len(lines) > maxEntries {
		// Keep last maxEntries lines
		lines = lines[len(lines)-maxEntries:]
		newContent := strings.Join(lines, "\n")

		return os.WriteFile(path, []byte(newContent), 0600)
	}

	return nil
}
