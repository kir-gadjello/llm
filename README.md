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

## Configuration

Create `~/.llmterm.yaml` for model profiles:

```yaml
default: gpt-4o-mini

models:
  gpt-4o-mini:
    api_base: https://api.openai.com/v1
    temperature: 0.1
    
  o1-reasoning:
    model: o1
    api_base: https://openrouter.ai/api/v1
    reasoning_effort: high
    reasoning_exclude: false
    
  groq-llama3:
    model: llama3-8b-8192
    api_base: https://api.groq.com/openai/v1
    
  local-llama:
    model: llama-3-8b-Instruct-q6
    api_base: http://localhost:8080/v1
```

Use with `-m <profile>`. CLI flags override config values.

**Reasoning parameters:**
- `reasoning_effort`: none, low, medium, high
- `reasoning_max_tokens`: integer token budget
- `reasoning_exclude`: exclude reasoning from response
- `context_order`: prepend, append (for clipboard)

## Compatibility

OpenAI-compatible endpoints only. Tested and working with:
- llama.cpp server
- tabbyAPI
- Groq API
- OpenRouter (with reasoning token support)
