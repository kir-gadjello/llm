package main

import (
	"bufio"
	"fmt"

	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"os"
)

func is_interactive(fd uintptr) bool {
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

type LLMChatRequest struct {
	Model       string                 `json:"model"`
	Seed        int                    `json:"seed"`
	Temperature float64                `json:"temperature"`
	Stream      bool                   `json:"stream"`
	Messages    []Message              `json:"messages"`
	Extra       map[string]interface{} `json:"-"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func llmChat(
	messages []Message,
	model string,
	seed int,
	temperature float64,
	postprocess func(string) string,
	apiKey string,
	apiBase string,
	stream bool,
	extra map[string]interface{},
	verbose bool,
) (<-chan string, error) {
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	if apiKey == "" && strings.Contains(apiBase, "api.openai.com") {
		return nil, fmt.Errorf("must provide OpenAI API key")
	}

	url := os.Getenv("OPENAI_API_BASE")
	if url == "" {
		url = apiBase
	}
	url = strings.TrimSuffix(url, "/")

	headers := http.Header{
		"Authorization": {"Bearer " + apiKey},
		"Content-Type":  {"application/json"},
	}

	req := LLMChatRequest{
		Model:       model,
		Seed:        seed,
		Temperature: temperature,
		Stream:      stream,
		Messages:    messages,
		Extra:       extra,
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// fmt.Println(req)

	var client *http.Client

	if verbose {
		client = &http.Client{
			Transport: &loggingTransport{},
		}
	} else {
		client = &http.Client{}
	}

	var resp *http.Response

	if stream {
		headers.Set("Accept", "text/event-stream")
		httpReq, err := http.NewRequest("POST", url+"/chat/completions", bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}
		httpReq.Header = headers
		resp, err = client.Do(httpReq)

		if err != nil {
			return nil, err
		}

		ch := make(chan string)

		go func() {
			scanner := bufio.NewScanner(resp.Body)
			scanner.Split(bufio.ScanLines)

			for scanner.Scan() {
				line := scanner.Text()

				// fmt.Println(line)

				if err != nil {
					if err != io.EOF {
						fmt.Println(err)
					}
					break
				}

				line = strings.TrimSpace(line)

				if strings.HasPrefix(line, "data: ") {
					// fmt.Println(line)

					var resp struct {
						Choices []struct {
							Delta struct {
								Content string `json:"content"`
							} `json:"delta"`
							FinishReason string `json:"finish_reason"`
							Index        int    `json:"index"`
						} `json:"choices"`
						Created int    `json:"created"`
						ID      string `json:"id"`
						Model   string `json:"model"`
						Object  string `json:"object"`
						Usage   struct {
							CompletionTokens int `json:"completion_tokens"`
							PromptTokens     int `json:"prompt_tokens"`
							TotalTokens      int `json:"total_tokens"`
						} `json:"usage,omitempty"` // add omitempty to avoid error when usage is not present
					}

					err := json.Unmarshal([]byte(line[6:]), &resp)

					if err != nil {
						fmt.Println(err)
						continue
					}

					// fmt.Println(resp)

					if resp.Choices[0].Delta.Content != "" {
						content := resp.Choices[0].Delta.Content
						// fmt.Println(content)
						if postprocess != nil {
							content = postprocess(content)
						}
						ch <- content
					} else if resp.Choices[0].FinishReason == "stop" {
						close(ch)
						return
					} else {
						fmt.Println("Unexpected end of chat completion stream:", resp)
					}
				}
			}

			close(ch)

			resp.Body.Close()
		}()

		return ch, nil
	}

	// println(url + "/chat/completions")

	httpReq, err := http.NewRequest("POST", url+"/chat/completions", bytes.NewBuffer(jsonData))

	if err != nil {
		return nil, err
	}

	httpReq.Header = headers

	resp, err = client.Do(httpReq)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var respBody struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	err = json.NewDecoder(resp.Body).Decode(&respBody)
	if err != nil {
		return nil, err
	}

	content := respBody.Choices[0].Message.Content
	if postprocess != nil {
		content = postprocess(content)
	}

	ch := make(chan string, 1) // create a buffered channel with capacity 1
	ch <- content
	close(ch)

	return ch, nil
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "llm-chat",
		Short: "LLM Chat CLI tool",
		RunE:  runLLMChat,
	}

	var is_terminal bool = is_interactive(os.Stdout.Fd())

	rootCmd.Flags().StringP("model", "m", "gpt-3.5-turbo", "LLM model")
	rootCmd.Flags().StringP("prompt", "p", "", "System prompt")
	rootCmd.Flags().IntP("seed", "s", 1337, "Random seed")
	rootCmd.Flags().Float64P("temperature", "t", 0.0, "Temperature")
	rootCmd.Flags().StringP("api-key", "k", "", "OpenAI API key")
	rootCmd.Flags().StringP("api-base", "b", "https://api.openai.com/v1/", "OpenAI API base URL")
	rootCmd.Flags().BoolP("stream", "S", is_terminal, "Stream output")
	rootCmd.Flags().BoolP("verbose", "v", false, "http logging")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runLLMChat(cmd *cobra.Command, args []string) error {
	model, _ := cmd.Flags().GetString("model")
	seed, _ := cmd.Flags().GetInt("seed")
	temperature, _ := cmd.Flags().GetFloat64("temperature")
	apiKey, _ := cmd.Flags().GetString("api-key")
	apiBase, _ := cmd.Flags().GetString("api-base")
	stream, _ := cmd.Flags().GetBool("stream")
	verbose, _ := cmd.Flags().GetBool("v")
	systemPrompt, _ := cmd.Flags().GetString("prompt")

	messages := make([]Message, 0)

	if len(strings.TrimSpace(systemPrompt)) > 0 {
		messages = append(messages, Message{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	var usermsg string = ""

	for _, arg := range args {
		usermsg += arg
	}

	// Read from stdin if available
	stat, _ := os.Stdin.Stat()
	var first = false
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		// stdin is a pipe or a file, read from it
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if first {
				usermsg += " "
				first = false
			}
			usermsg += scanner.Text()
		}
	}

	messages = append(messages, Message{
		Role:    "user",
		Content: usermsg,
	})

	ch, err := llmChat(messages, model, seed, temperature, nil, apiKey, apiBase, stream, nil, verbose)

	if err != nil {
		fmt.Println(err)
		return err
	}

	for content := range ch {
		fmt.Print(content)
	}

	return nil
}

type loggingTransport struct{}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	fmt.Printf(">>> %s %s %s\n", req.Method, req.URL, req.Proto)
	for k, v := range req.Header {
		fmt.Printf(">>> %s: %s\n", k, v)
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		fmt.Printf(">>> %s\n", body)
	}

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	fmt.Printf("<<< %s %s %s\n", resp.Status, resp.Proto, resp.Status)
	for k, v := range resp.Header {
		fmt.Printf("<<< %s: %s\n", k, v)
	}
	if resp.Body != nil {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Println(err)
			return nil, err
		}
		fmt.Printf("<<< %s\n", body)
	}

	return resp, nil
}
