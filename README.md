# llm

A CLI tool for using local and remote LLMs directly from the shell, with streaming, interactivity, and OpenRouter-style reasoning token support.

```bash
go install github.com/kir-gadjello/llm@latest
```

## Usage

```bash
llm "your message"
llm -p "system prompt" "user message"
echo "data" | llm "analyze this"
llm -c  # interactive chat
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
```

### Clipboard Integration

```bash
pbcopy < file.txt && llm -x "review this"
llm -x --context-order append "context after prompt"
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
- `reasoning_effort`: none, low, medium, high
- `reasoning_max_tokens`: integer token budget
- `reasoning_exclude`: exclude reasoning from response
- `context_order`: prepend, append (for clipboard)
- `extra_body`: arbitrary JSON fields for the API request

**Top-level parameters:**
- `default`: default model profile to use
- `piped_input_wrapper`: wrapper tag for piped stdin (default: "context", empty string disables)

## Compatibility

OpenAI-compatible endpoints only. Tested and working with:
- llama.cpp server
- tabbyAPI
- Groq API
- OpenRouter (with reasoning token support)
