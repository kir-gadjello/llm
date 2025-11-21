# llm

## Configuration

Optional `~/.llmterm.yaml`:

```yaml
default: gpt-4o-mini

models:
  gpt-4o-mini:
    api_base: https://api.openai.com/v1
    temperature: 0.1
  groq-llama3:
    model: llama3-8b-8192
    api_base: https://api.groq.com/openai/v1
  local-llama:
    model: llama-3-8b-Instruct-q6
    api_base: http://localhost:8080/v1
```

CLI flags override config. Use `-m <profile>` or default.

## Synopsis

A cli tool to make local and remote LLMs useful in the shell (bonus: streaming & interactivity supported) 

Only OpenAI-compatible endpoints are supported and detected via environment variable `OPENAI_API_BASE`. llama.cpp server, tabbyAPI and Groq API are supported and tested. 

The tool is suitable for basic usage as is and I retain the right to add various useful features in the future.

Install: `go install github.com/kir-gadjello/llm@latest`

## Examples

`llm <your user message>` \
`llm -p=<your system prompt> <your user message>` \
`some-program | llm <your user message>` - stdin pipe, also compatible with user prompts and system prompts \
`llm`, `llm -c` - interactive chat \
`llm -p='You are an intelligent AI assistant answering ONLY "yes" or "no" to all user questions and queries' -J '{"$schema": "http://json-schema.org/draft-07/schema#", "type": "string", "enum": ["yes", "no"]}' is pi larger than e` - json schema constrained generation (llama.cpp)
