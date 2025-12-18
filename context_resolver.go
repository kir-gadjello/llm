package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// PathResolver handles @ syntax parsing and git integration
type PathResolver struct {
	verbose bool
}

// NewPathResolver creates a new path resolver
func NewPathResolver(verbose bool) *PathResolver {
	return &PathResolver{verbose: verbose}
}

// ParsePrompt extracts @tokens from user input and returns cleaned prompt + paths
func (pr *PathResolver) ParsePrompt(input string) (string, []string) {
	// Regex to match @word or @path/to/file patterns
	re := regexp.MustCompile(`@([\w/.-]+)`)

	matches := re.FindAllStringSubmatch(input, -1)
	paths := make([]string, 0)

	for _, match := range matches {
		if len(match) > 1 {
			paths = append(paths, match[1])
		}
	}

	// Remove @ tokens from prompt
	cleanedPrompt := re.ReplaceAllString(input, "")

	// Clean up extra whitespace
	cleanedPrompt = strings.TrimSpace(cleanedPrompt)
	cleanedPrompt = regexp.MustCompile(`\s+`).ReplaceAllString(cleanedPrompt, " ")

	return cleanedPrompt, paths
}

// isGitRepo checks if current directory is in a git repository
func (pr *PathResolver) isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	err := cmd.Run()
	return err == nil
}

// ExpandGit expands git aliases like @staged, @dirty, @last
func (pr *PathResolver) ExpandGit(alias string) ([]string, error) {
	var cmd *exec.Cmd

	switch alias {
	case "staged":
		cmd = exec.Command("git", "diff", "--name-only", "--cached")
	case "dirty":
		cmd = exec.Command("git", "diff", "--name-only")
	case "last":
		cmd = exec.Command("git", "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	default:
		return nil, fmt.Errorf("unknown git alias: @%s", alias)
	}

	// Check if we're in a git repo
	if !pr.isGitRepo() {
		return nil, fmt.Errorf("not in a git repository (required for @%s)", alias)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("git command failed for @%s: %v - %s", alias, err, stderr.String())
	}

	// Parse output
	paths := make([]string, 0)
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to parse git output: %w", err)
	}

	return paths, nil
}

// ExpandGlob expands glob patterns like src/*.go
func (pr *PathResolver) ExpandGlob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern %s: %w", pattern, err)
	}

	// Filter out directories if any snuck in
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			if pr.verbose {
				fmt.Fprintf(os.Stderr, "Warning: cannot stat %s: %v\n", match, err)
			}
			continue
		}
		if !info.IsDir() {
			files = append(files, match)
		}
	}

	return files, nil
}

// ExpandDirectory walks a directory and collects all files
func (pr *PathResolver) ExpandDirectory(dirPath string) ([]string, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", dirPath, err)
	}

	if !info.IsDir() {
		return []string{dirPath}, nil
	}

	files := make([]string, 0)

	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if pr.verbose {
				fmt.Fprintf(os.Stderr, "Warning: error walking %s: %v\n", path, err)
			}
			return nil // continue walking
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		files = append(files, path)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", dirPath, err)
	}

	return files, nil
}

func (pr *PathResolver) shouldIgnore(path string, useGitignore bool, useDefaultIgnore bool) bool {
	base := filepath.Base(path)
	if useDefaultIgnore {
		// Typical large or unsuitable files
		defaults := []string{"package-lock.json", "yarn.lock", "pnpm-lock.yaml", ".DS_Store", "node_modules", ".git"}
		for _, d := range defaults {
			if base == d || strings.Contains(path, "/"+d+"/") {
				return true
			}
		}
	}

	if useGitignore {
		// Staff level check: look for .gitignore in parent directories
		dir := filepath.Dir(path)
		for {
			giPath := filepath.Join(dir, ".gitignore")
			if _, err := os.Stat(giPath); err == nil {
				data, _ := os.ReadFile(giPath)
				lines := strings.Split(string(data), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					// Simple match
					if matched, _ := filepath.Match(line, base); matched {
						return true
					}
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return false
}

// Resolve takes a list of path patterns and expands them to actual file paths
func (pr *PathResolver) Resolve(patterns []string, useGitignore bool, useDefaultIgnore bool) ([]string, error) {
	allPaths := make([]string, 0)
	gitAliases := map[string]bool{"staged": true, "dirty": true, "last": true}

	var errors []string
	var normalizedPatterns []string
	for _, p := range patterns {
		normalizedPatterns = append(normalizedPatterns, strings.Split(p, ",")...)
	}

	for _, pattern := range normalizedPatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		if gitAliases[pattern] {
			paths, err := pr.ExpandGit(pattern)
			if err != nil {
				errors = append(errors, err.Error())
				continue
			}
			allPaths = append(allPaths, paths...)
			continue
		}

		// Staff recursive globbing implementation
		if strings.ContainsAny(pattern, "*?[]") {
			// If it's a simple glob in current dir
			if !strings.Contains(pattern, "/") && !strings.Contains(pattern, "\\") {
				matches, _ := filepath.Glob(pattern)
				allPaths = append(allPaths, matches...)
				continue
			}
		}

		// Handle dirs and direct files
		info, err := os.Stat(pattern)
		if err != nil {
			// Might be a glob in a subfolder or doesn't exist
			matches, globErr := filepath.Glob(pattern)
			if globErr == nil && len(matches) > 0 {
				allPaths = append(allPaths, matches...)
				continue
			}
			errors = append(errors, fmt.Sprintf("cannot access %s: %v", pattern, err))
			continue
		}

		if info.IsDir() {
			err = filepath.Walk(pattern, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !info.IsDir() {
					if !pr.shouldIgnore(path, useGitignore, useDefaultIgnore) {
						allPaths = append(allPaths, path)
					}
				}
				return nil
			})
		} else {
			allPaths = append(allPaths, pattern)
		}
	}

	// Filter and Deduplicate
	seen := make(map[string]bool)
	unique := make([]string, 0, len(allPaths))

	for _, p := range allPaths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			if pr.verbose {
				fmt.Fprintf(os.Stderr, "Warning: failed to get absolute path for %s: %v\n", p, err)
			}
			absPath = p
		}

		if !seen[absPath] {
			seen[absPath] = true
			unique = append(unique, absPath)
		}
	}

	// If we had errors but also got some paths, just warn
	if len(errors) > 0 && len(unique) == 0 {
		return nil, fmt.Errorf("failed to resolve paths: %s", strings.Join(errors, "; "))
	}

	if len(errors) > 0 && pr.verbose {
		fmt.Fprintf(os.Stderr, "Warnings during path resolution: %s\n", strings.Join(errors, "; "))
	}

	return unique, nil
}
