# llm

A CLI tool for using local and remote LLMs directly from the shell, with streaming, interactivity, and OpenRouter-style reasoning token support.

```bash
go install github.com/kir-gadjello/llm@latest
```

## Usage

```bash
llm "your message"
llm -p "system prompt" "user message"
llm -C "user message" # start chat and send immediately
echo "data" | llm "analyze this"
llm -c  # interactive chat
llm history # browse recent chats
llm search "query"  # search history
```

### Session Mode

Wrap your shell in an AI harness. Type `??` before any question to invoke the LLM with full terminal context:

```bash
llm session
# or: llm --session

?? why did that last command fail
?? show me all git branches sorted by date
```

The LLM receives structured command history with outputs and exit codes. For better context tracking, enable shell integration:

```bash
# Add to ~/.zshrc, ~/.bashrc, or ~/.config/fish/config.fish
source <(llm integration zsh)   # or bash, fish
```

Shell integration uses OSC 133 sequences to parse command boundaries, providing clean structured history instead of raw terminal output.

### History & Session Management

**Search History**
Query your local conversation database using full-text search (requires FTS5 support):

```bash
llm search "database migration"
llm search "user:optimization"  # Filter by role
```

**Resume Session**
Continue a conversation from a specific session UUID (found via search):

```bash
llm resume <uuid> "continue explaining the previous point"
```

### Shell Assistant

Generate and execute shell commands using natural language. Detects your shell and OS for precise syntax:

```bash
llm -s "find all typescript files excluding node_modules"
# Generates: find . -type f -name "*.ts" ! -path "*/node_modules/*"
```

**Interactive Menu**
- **Execute**: Run the command immediately
- **Revise**: Refine with more natural language
- **Describe**: Get a detailed explanation
- **Copy**: Copy to clipboard

**YOLO Mode**
Skip confirmation for automation:
```bash
llm -s -y "git branch --show-current"
```

**Shell History Context**
Include recent commands for better context-aware generation:
```bash
llm -s -H "undo that"  # includes last 20 commands
llm -s -H5 "fix the error"  # last 5 commands
```

### Reasoning Models

Control reasoning token generation for models that support it (OpenAI o-series, Grok, etc.):

```bash
llm -m o1 --reasoning-high "explain quantum entanglement"
llm -m grok-2 -n "simple task"  # disable reasoning
llm -R2048 "complex analysis"   # specific token budget
llm --reasoning-exclude         # use reasoning but exclude it from output
```

### Clipboard Integration

```bash
pbcopy < file.txt && llm -x "review this"
llm -x --context-order append "context after prompt"
```

### Context Formatting

Control how files are presented to the model:

```bash
# Show relative paths (default)
llm -f main.go

# Hide filenames
llm -f main.go --show-filenames none

# Use XML format instead of Markdown
llm -f main.go --context-format xml
```

### Constrained Generation

JSON schema support for llama.cpp and compatible backends:

```bash
llm -J '{"type": "string", "enum": ["yes", "no"]}' "is pi > e"
```

### Piped Input

By default, piped stdin content is wrapped with `<context>` tags for clarity:

```bash
cat file.txt | llm "summarize this"
# Sends: <context>\n{file contents}\n</context>\n\nsummarize this

# Use custom wrapper tag
echo "data" | llm -w "input" "analyze"

# Disable wrapping
echo "data" | llm -w "" "analyze"
```

### File Context & Auto-Selection

Include code files and let the LLM intelligently select relevant files based on your query.

**Manual File Selection**

```bash
# Include specific files
llm -f src/main.go -f lib/utils.go "explain the architecture"

# Include directories (walks and loads all files)
llm -f src/ "review the codebase"

# Use glob patterns
llm -f "src/**/*.go" "find all error handling"
```

**Git Integration with @ Syntax**

Reference files from git context directly in your prompt:

```bash
# Review staged changes
llm "@staged review these changes before commit"

# Analyze uncommitted modifications
llm "@dirty what bugs might these changes introduce"

# Examine last commit
llm "@last explain what this commit does"

# Reference specific files with @ prefix
llm "@src/main.go @lib/config.go how do these interact"

# Combine git aliases with manual files
llm -f README.md "@staged document these changes"
```

**Git Aliases:**
- `@staged` - Files in git staging area (`git diff --name-only --cached`)
- `@dirty` - Modified but unstaged files (`git diff --name-only`)
- `@last` - Files changed in last commit (`git diff-tree --no-commit-id --name-only -r HEAD`)

**Auto-Selection Mode (-A)**

Let the LLM automatically select relevant files using a repository map:

```bash
# Auto-select files based on query
llm -A "find the session parser implementation"

# Combine auto-selection with manual files
llm -A -f config.yaml "how is authentication configured"

# Works with git syntax too
llm -A "@staged ensure these changes don't break auth"
```

**How Auto-Mode Works:**
1. Generates a structural map of your repository using tree-sitter
2. Sends the map + your query to an LLM (configurable model)
3. LLM returns relevant file paths as JSON
4. Files are loaded and included in context
5. Shows selected files: `reviewed: main.go, auth.go, session.go`

**Binary File Handling**

Binary files are automatically detected and replaced with `[Binary File]` placeholders to avoid sending garbage to the LLM.

## Debugging & Diagnostics

**System Check**
Verify installation health, config paths, and FTS5 support:

```bash
llm doctor
```

**Performance Metrics**
Show Time-To-First-Token (TTFT) and generation speed (TPS):

```bash
llm --vt "count to 100"
```

**Dry Run**
Preview prompt assembly, token estimation, and API parameters without making a network request:

```bash
llm --dry -f src/main.go "explain this"
```

## Configuration

Create `~/.llmterm.yaml` for model profiles with inheritance:

```yaml
default: grok

piped_input_wrapper: "data"

models:
  # Base configuration for OpenRouter
  _openrouter:
    api_base: https://openrouter.ai/api/v1
    extra_body:
      include_reasoning: true
      stream_options:
        include_usage: true

  # Model aliases that extend base configs
  grok:
    extend: _openrouter
    model: x-ai/grok-4.1-fast
    reasoning_effort: low
    
  # Full model names can also be aliases
  x-ai/grok-4.1-fast:
    extend: grok
    
  codex:
    extend: _openrouter
    model: gpt-5.1-codex
    reasoning_effort: high
    
  local-llama:
    model: llama-3-8b-Instruct-q6
    api_base: http://localhost:8080/v1

# File context and auto-selection settings
context:
  auto_selector_model: "x-ai/grok-4.1-fast"  # Model for -A flag (empty = use main model)
  max_file_size_kb: 1024                      # Skip files larger than this
  max_repo_files: 1000                        # Max files in repo map
  ignored_dirs: [".git", "node_modules", "dist", "vendor", "__pycache__"]
  debug_truncate_files: 10                    # Truncate file context in debug output
```


Use with `-m <profile>`. CLI flags override config values.

**Config inheritance:**
- Use `extend: <parent>` to inherit from another profile
- Child values override parent values
- `extra_body` maps are deep-merged
- Create aliases for full model names (like `x-ai/grok-4.1-fast`)

**Profile parameters:**
- `model`: actual model name sent to API
- `api_base`, `api_key`: endpoint configuration
- `reasoning_effort`: none, low, medium, high, xhigh
- `reasoning_max_tokens`: integer token budget
- `reasoning_exclude`: exclude reasoning from response
- `verbosity`: low, medium, high
- `context_order`: prepend, append (for clipboard)
- `extra_body`: arbitrary JSON fields for the API request

**Top-level parameters:**
- `default`: default model profile to use
- `piped_input_wrapper`: wrapper tag for piped stdin (default: "context", empty string disables)

**Context configuration (`context`):**
- `auto_selector_model`: model to use for `-A` auto-selection (empty = use main model)
- `max_file_size_kb`: maximum file size to load (default: 1024 KB)
- `max_repo_files`: maximum number of files to include in repo map (default: 1000)
- `ignored_dirs`: directories to skip when generating repo maps
- `debug_truncate_files`: number of lines to show per file in debug output (default: 10)

## Compatibility

OpenAI-compatible endpoints only. Tested and working with:
- llama.cpp server
- tabbyAPI
- Groq API
- OpenRouter (with reasoning token support)
