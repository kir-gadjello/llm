package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

type StreamEvent struct {
	Type    string // "content" or "reasoning"
	Content string
}

type LLMChatRequestBasic struct {
	Model       string                 `json:"model"`
	Seed        int                    `json:"seed"`
	Temperature *float64               `json:"temperature,omitempty"`
	Stream      bool                   `json:"stream"`
	Messages    []LLMMessage           `json:"messages"`
	Extra       map[string]interface{} `json:"-"`
}

type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func resolveLLMApi(apiKey string, apiBase string) (string, string, error) {
	// If apiKey is already set (from config), use it
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	url := os.Getenv("OPENAI_API_BASE")
	if url == "" {
		url = apiBase
	}
	url = strings.TrimSuffix(url, "/")

	return apiKey, url, nil
}

func urlJoin(base, rel string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}

	relURL, err := url.Parse(rel)
	if err != nil {
		return "", err
	}

	if relURL.Scheme != "" && relURL.Host != "" {
		return rel, nil
	}

	joinedPath := path.Join(baseURL.Path, relURL.Path)

	result := &url.URL{
		Scheme: baseURL.Scheme,
		User:   baseURL.User,
		Host:   baseURL.Host,
		Path:   joinedPath,
	}

	return result.String(), nil
}

func llmChat(
	ctx context.Context,
	messages []LLMMessage,
	model string,
	seed int,
	temperature *float64,
	postprocess func(string) string,
	apiKey string,
	apiBase string,
	stream bool,
	extra map[string]interface{},
	verbose bool,
) (<-chan StreamEvent, error) {
	apiKey, apiBase, err := resolveLLMApi(apiKey, apiBase)
	if err != nil {
		return nil, err
	}

	headers := http.Header{
		"Authorization": {"Bearer " + apiKey},
		"Content-Type":  {"application/json"},
	}

	req := LLMChatRequestBasic{
		Model:       model,
		Seed:        seed,
		Temperature: temperature,
		Stream:      stream,
		Messages:    messages,
	}

	mergedData := map[string]interface{}{}

	reqJson, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(reqJson, &mergedData)
	if err != nil {
		return nil, err
	}

	for k, v := range extra {
		mergedData[k] = v
	}

	jsonData, err := json.Marshal(mergedData)
	if err != nil {
		return nil, err
	}

	var client *http.Client

	if verbose {
		client = &http.Client{
			Transport: &loggingTransport{},
		}
	} else {
		client = &http.Client{}
	}

	if verbose {
		fmt.Printf("REQ: %s\n", jsonData)
	}

	var resp *http.Response

	chatUrl, err := urlJoin(apiBase, "/chat/completions")
	if err != nil {
		return nil, err
	}

	if stream {
		headers.Set("Accept", "text/event-stream")
		httpReq, err := http.NewRequestWithContext(ctx, "POST", chatUrl, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}
		httpReq.Header = headers
		resp, err = client.Do(httpReq)

		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
		}

		ch := make(chan StreamEvent)

		go func() {
			scanner := bufio.NewScanner(resp.Body)
			scanner.Split(bufio.ScanLines)

			for scanner.Scan() {
				line := scanner.Text()

				if err != nil {
					if err != io.EOF {
						fmt.Println(err)
					}
					break
				}

				line = strings.TrimSpace(line)

				if strings.HasPrefix(line, "data: ") {
					dataStr := strings.TrimSpace(line[6:])
					if dataStr == "[DONE]" {
						close(ch)
						return
					}

					var resp struct {
						Choices []struct {
							Delta struct {
								Content   string `json:"content"`
								Reasoning string `json:"reasoning"`
							} `json:"delta"`
							FinishReason *string `json:"finish_reason"`
							Index        int     `json:"index"`
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

					if len(resp.Choices) > 0 {
						if resp.Choices[0].Delta.Reasoning != "" {
							ch <- StreamEvent{Type: "reasoning", Content: resp.Choices[0].Delta.Reasoning}
						}
						if resp.Choices[0].Delta.Content != "" {
							content := resp.Choices[0].Delta.Content
							if postprocess != nil {
								content = postprocess(content)
							}
							ch <- StreamEvent{Type: "content", Content: content}
						}

						if resp.Choices[0].FinishReason != nil && len(*resp.Choices[0].FinishReason) > 0 {
							// close(ch) happens at end of loop
						}
					}
				}
			}

			close(ch)

			resp.Body.Close()
		}()

		return ch, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", chatUrl, bytes.NewBuffer(jsonData))

	if err != nil {
		return nil, err
	}

	httpReq.Header = headers

	resp, err = client.Do(httpReq)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

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

	if len(respBody.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned from API")
	}

	content := respBody.Choices[0].Message.Content
	if postprocess != nil {
		content = postprocess(content)
	}

	ch := make(chan StreamEvent, 1) // create a buffered channel with capacity 1
	ch <- StreamEvent{Type: "content", Content: content}
	close(ch)

	return ch, nil
}

type Model struct {
	ID   string                 `json:"id"`
	Meta map[string]interface{} `json:"meta"`
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

func getModelList(apiKey string, apiBase string, timeout time.Duration) ([]Model, error) {

	url, err := urlJoin(apiBase, "models")
	if err != nil {
		return nil, err
	}

	headers := http.Header{
		"Authorization": {"Bearer " + apiKey},
		"Content-Type":  {"application/json"},
	}

	client := &http.Client{
		Timeout: timeout, // set the timeout for the client
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = headers

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var modelList ModelList
	err = json.NewDecoder(resp.Body).Decode(&modelList)
	if err != nil {
		return nil, err
	}

	return modelList.Data, nil
}

type loggingTransport struct{}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	fmt.Printf(">>> %s %s %s\n", req.Method, req.URL, req.Proto)
	for k, v := range req.Header {
		fmt.Printf(">>> %s: %s\n", k, v)
	}

	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewBuffer(reqBody))

	var jsonData interface{}
	err = json.Unmarshal(reqBody, &jsonData)
	if err == nil {
		jsonBytes, _ := json.MarshalIndent(jsonData, "", "  ")
		fmt.Printf(">>> %s\n", jsonBytes)
	} else {
		fmt.Printf(">>> %s\n", reqBody)
	}

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	fmt.Printf("<<< %s %s %s\n", resp.Status, resp.Proto, resp.Status)
	for k, v := range resp.Header {
		fmt.Printf("<<< %s: %s\n", k, v)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewBuffer(respBody))
	defer resp.Body.Close()

	var jsonDataResp interface{}
	err = json.Unmarshal(respBody, &jsonDataResp)
	if err == nil {
		jsonBytes, _ := json.MarshalIndent(jsonDataResp, "", "  ")
		fmt.Printf("<<< %s\n", jsonBytes)
	} else {
		fmt.Printf("<<< %s\n", respBody)
	}

	return resp, nil
}
