package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPersistence(t *testing.T) {
	if llmBinaryPath == "" {
        // Assume built by TestMain
    }

	server := httptest.NewServer(MockEchoHandler())
	defer server.Close()

	tempHome, _ := os.MkdirTemp("", "llm-persist-home")
	defer os.RemoveAll(tempHome)

    runLLM := func(args ...string) string {
        cmd := exec.Command(llmBinaryPath, args...)
        cmd.Env = append(os.Environ(),
            fmt.Sprintf("HOME=%s", tempHome),
            fmt.Sprintf("OPENAI_API_BASE=%s", server.URL),
            "OPENAI_API_KEY=dummy",
            "TERM=dumb",
        )
        out, err := cmd.CombinedOutput()
        if err != nil {
            t.Logf("Command %v failed: %v\nOutput: %s", args, err, out)
        }
        return string(out)
    }

    // 1. Start Session
    unique1 := "unique_msg_alpha"
    runLLM(unique1)

    // 2. Get UUID
    outHist := runLLM("history")
    lines := strings.Split(strings.TrimSpace(outHist), "\n")
    if len(lines) == 0 {
        t.Fatal("History empty after first message")
    }
    parts := strings.Fields(lines[0])
    if len(parts) < 1 {
        t.Fatal("Invalid history output")
    }
    uuid := parts[0]

    // 3. Resume Session
    unique2 := "unique_msg_beta"
    runLLM("resume", uuid, unique2)

    // 4. Verify Persistence
    
    // A. Check if unique1 is in history (User Msg)
    outSearch1 := runLLM("search", unique1)
    if strings.Contains(outSearch1, "No matches found") || !strings.Contains(outSearch1, uuid[:8]) {
         t.Errorf("Failed to find first message '%s' in history. Output:\n%s", unique1, outSearch1)
    }

    // B. Check if unique2 is in history (User Msg - Resume)
    outSearch2 := runLLM("search", unique2)
    if strings.Contains(outSearch2, "No matches found") || !strings.Contains(outSearch2, uuid[:8]) {
         t.Errorf("Failed to find second message '%s' in history (Resume persistence failed). Output:\n%s", unique2, outSearch2)
    }

    // C. Check if Assistant Response is in history
    outSearch3 := runLLM("search", "model")
    if strings.Contains(outSearch3, "No matches found") || !strings.Contains(outSearch3, uuid[:8]) {
        t.Errorf("Failed to find assistant response (JSON echo) in history (Response persistence failed). Output:\n%s", outSearch3)
    }
}
