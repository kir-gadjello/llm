package main

import (
	"fmt"
)

// ShellIntegrationScripts contains the shell scripts to enable OSC 133 integration
var ShellIntegrationScripts = map[string]string{
	"zsh": `
# llm-term shell integration for Zsh
# Emits OSC 133 sequences for prompt/command tracking

__llm_term_precmd() {
    local ret=$?
    # OSC 133;D;{exit_code} ST - Command Finished
    printf "\033]133;D;%d\007" $ret
    # OSC 133;A ST - Prompt Start
    printf "\033]133;A\007"
}

__llm_term_preexec() {
    # OSC 133;C ST - Output Start (Command Executed)
    printf "\033]133;C\007"
}

# Hook into zsh
autoload -Uz add-zsh-hook
add-zsh-hook precmd __llm_term_precmd
add-zsh-hook preexec __llm_term_preexec
`,
	"bash": `
# llm-term shell integration for Bash
# Emits OSC 133 sequences for prompt/command tracking

__llm_term_precmd() {
    local ret=$?
    # OSC 133;D;{exit_code} ST - Command Finished
    printf "\033]133;D;%d\007" $ret
    # OSC 133;A ST - Prompt Start
    printf "\033]133;A\007"
}

__llm_term_preexec() {
    # OSC 133;C ST - Output Start
    printf "\033]133;C\007"
}

# Hook into bash via PROMPT_COMMAND and DEBUG trap
# This is a simplified version; robust bash integration is tricky.
# We use a simple approach: PROMPT_COMMAND runs before prompt.
# We need a way to detect "before execution".
# Bash 4.4+ has PS0.

if [[ -n "$PS0" ]]; then
    PS0="\[\033]133;C\007\]$PS0"
else
    # Fallback for older bash (less reliable for output start)
    :
fi

# Append to existing PROMPT_COMMAND
PROMPT_COMMAND="__llm_term_precmd; $PROMPT_COMMAND"
`,
	"fish": `
# llm-term shell integration for Fish
# Emits OSC 133 sequences for prompt/command tracking

function __llm_term_precmd --on-event fish_prompt
    set -l last_status $status
    # OSC 133;D;{exit_code} ST
    printf "\033]133;D;%d\007" $last_status
    # OSC 133;A ST
    printf "\033]133;A\007"
end

function __llm_term_preexec --on-event fish_preexec
    # OSC 133;C ST
    printf "\033]133;C\007"
end
`,
}

func printShellIntegration(shell string) error {
	script, ok := ShellIntegrationScripts[shell]
	if !ok {
		return fmt.Errorf("unsupported shell: %s (supported: zsh, bash, fish)", shell)
	}
	fmt.Println(script)
	return nil
}
