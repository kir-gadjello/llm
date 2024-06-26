package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	markdown "github.com/vlanse/go-term-markdown"
)

var TEXTINPUT_PLACEHOLDER = "Type a message and press Enter to send..."

func is_interactive(fd uintptr) bool {
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

type LLMChatRequestBasic struct {
	Model       string                 `json:"model"`
	Seed        int                    `json:"seed"`
	Temperature float64                `json:"temperature"`
	Stream      bool                   `json:"stream"`
	Messages    []LLMMessage           `json:"messages"`
	Extra       map[string]interface{} `json:"-"`
}

type Message struct {
	UUID    string `json:"uuid"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewMessage(role, content string) *Message {
	uuid := generateUUID()

	return &Message{
		UUID:    uuid,
		Role:    role,
		Content: content,
	}
}

func resolveLLMApi(apiKey string, apiBase string) (string, string, error) {
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	if apiKey == "" && strings.Contains(apiBase, "api.openai.com") {
		return "", "", fmt.Errorf("must provide OpenAI API key")
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
	messages []LLMMessage,
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
	apiKey, apiBase, err := resolveLLMApi(apiKey, apiBase)
	if err != nil {
		log.Fatal(err)
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
		httpReq, err := http.NewRequest("POST", chatUrl, bytes.NewBuffer(jsonData))
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

				if err != nil {
					if err != io.EOF {
						fmt.Println(err)
					}
					break
				}

				line = strings.TrimSpace(line)

				if strings.HasPrefix(line, "data: ") {

					var resp struct {
						Choices []struct {
							Delta struct {
								Content string `json:"content"`
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

					if resp.Choices[0].Delta.Content != "" {
						content := resp.Choices[0].Delta.Content
						if postprocess != nil {
							content = postprocess(content)
						}
						ch <- content
					} else {
						if resp.Choices[0].FinishReason != nil && len(*resp.Choices[0].FinishReason) > 0 {
							close(ch)
							return
						} else {
							if verbose {
								fmt.Println("Unexpected end of chat completion stream:", line)
							}
						}
					}
				}
			}

			close(ch)

			resp.Body.Close()
		}()

		return ch, nil
	}

	httpReq, err := http.NewRequest("POST", chatUrl, bytes.NewBuffer(jsonData))

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

func putTextIntoClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin": // macOS
		cmd := exec.Command("pbcopy")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Start()
		if err != nil {
			return err
		}
		_, err = stdin.Write([]byte(text))
		if err != nil {
			return err
		}
		err = stdin.Close()
		if err != nil {
			return err
		}
		err = cmd.Wait()
		if err != nil {
			return err
		}
		return nil
	case "linux":
		cmd = exec.Command("xclip", "-selection", "clipboard", text)
		return cmd.Run()
	case "windows":
		cmd = exec.Command("clip", text)
		return cmd.Run()
	default:
		return errors.New("unsupported OS")
	}
}

type Session struct {
	UUID      string
	Timestamp time.Time
}

func newSession() *Session {
	uuid := generateUUID()
	return &Session{UUID: uuid, Timestamp: time.Now()}
}

func generateUUID() string {
	u := make([]byte, 16)
	_, err := rand.Read(u)
	if err != nil {
		return fmt.Sprintf("%s", time.Now().UnixMilli())
	}
	return base64.URLEncoding.EncodeToString(u)
}

func dumpToHistory(session *Session, data interface{}) error {
	configDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	historyDir := filepath.Join(configDir, ".config/llmcli")

	if _, err := os.Stat(historyDir); os.IsNotExist(err) {
		if err := os.MkdirAll(historyDir, 0o755); err != nil {
			return err
		}
	}
	historyFile := filepath.Join(historyDir, "history.jsonl")
	f, err := os.OpenFile(historyFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	jsonString := string(jsonData) + "\n"
	_, err = f.WriteString(jsonString)
	return err
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "llm-chat",
		Short: "LLM Chat CLI tool",
		RunE:  runLLMChat,
	}

	var is_terminal bool = is_interactive(os.Stdout.Fd())

	rootCmd.Flags().StringP("model", "m", "", "LLM model: OPENAI_API_MODEL,GROQ_API_MODEL,LLM_MODEL from env or gpt-3.5-turbo")
	rootCmd.Flags().BoolP("chat", "c", false, "Launch chat mode")
	rootCmd.Flags().BoolP("chat-send", "C", false, "Launch chat mode and send the first message right away")
	rootCmd.Flags().StringP("prompt", "p", "", "System prompt")
	rootCmd.Flags().IntP("seed", "s", 1337, "Random seed")
	rootCmd.Flags().Float64P("temperature", "t", 0.0, "Temperature")
	rootCmd.Flags().IntP("max_tokens", "N", 4096, "Max amount of tokens in response")
	rootCmd.Flags().Float64P("frequency_penalty", "Q", 0.0, "Frequency penalty between -2.0 and 2.0")
	rootCmd.Flags().Float64P("presence_penalty", "Y", 0.0, "Presence penalty between -2.0 and 2.0")
	rootCmd.Flags().BoolP("json", "j", false, "json mode")
	rootCmd.Flags().StringP("json-schema", "J", "", "json schema (compatible with llama.cpp and tabbyAPI, not compatible with OpenAI)")
	rootCmd.Flags().StringP("stop", "X", "", "Stop sequences (a single word or a json array)")
	rootCmd.Flags().Float64P("top_p", "", 1.0, "Top-P sampling setting, defaults to 1.0")
	rootCmd.Flags().StringP("api-params", "A", "{}", "Additional LLM API parameters expressed as json, take precedence over provided CLI arguments")
	rootCmd.Flags().StringP("api-key", "k", "", "OpenAI API key")
	rootCmd.Flags().StringP("api-base", "b", "https://api.openai.com/v1/", "OpenAI API base URL")
	rootCmd.Flags().BoolP("stream", "S", is_terminal, "Stream output")
	rootCmd.Flags().BoolP("verbose", "v", false, "http & debug logging")
	rootCmd.Flags().StringSliceP("files", "f", []string{}, "List of files and directories to include in context")
	rootCmd.Flags().StringP("context-format", "i", "md", "Context (files) input template format (md|xml)")
	rootCmd.Flags().BoolP("debug", "D", false, "Output prompt & system msg")

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func markChatStart(session *Session, userMsg, systemPrompt, model string, seed int, temperature float64, apiBase string, maxTokens int, frequencyPenalty, presencePenalty float64, jsonMode bool, stopSequences interface{}, topP float64, apiParams string, jsonSchema string) error {
	data := struct {
		SID              string      `json:"sid"`
		TS               int         `json:"ts"`
		UserMsg          string      `json:"user_msg"`
		SystemPrompt     string      `json:"system_prompt"`
		Model            string      `json:"model"`
		Seed             int         `json:"seed"`
		Temperature      float64     `json:"temperature"`
		APIBase          string      `json:"api_base"`
		MaxTokens        int         `json:"max_tokens"`
		FrequencyPenalty float64     `json:"frequency_penalty"`
		PresencePenalty  float64     `json:"presence_penalty"`
		JSONMode         bool        `json:"json_mode"`
		StopSequences    interface{} `json:"stop_sequences"`
		TopP             float64     `json:"top_p"`
		APIParams        string      `json:"api_params"`
		JsonSchema       string      `json:"api_params"`
	}{
		SID:              session.UUID,
		TS:               int(time.Now().Unix()),
		UserMsg:          userMsg,
		SystemPrompt:     systemPrompt,
		Model:            model,
		Seed:             seed,
		Temperature:      temperature,
		APIBase:          apiBase,
		MaxTokens:        maxTokens,
		FrequencyPenalty: frequencyPenalty,
		PresencePenalty:  presencePenalty,
		JSONMode:         jsonMode,
		StopSequences:    stopSequences,
		TopP:             topP,
		APIParams:        apiParams,
		JsonSchema:       jsonSchema,
	}
	return dumpToHistory(session, data)
}

func getFirstEnv(fallback string, envVars ...string) string {
	for _, env := range envVars {
		v := os.Getenv(env)
		if v != "" {
			return v
		}
	}
	return fallback
}

func runLLMChat(cmd *cobra.Command, args []string) error {
	session := newSession()

	modelname, _ := cmd.Flags().GetString("model")

	if len(modelname) == 0 {
		modelname = getFirstEnv("gpt-3.5-turbo", "OPENAI_API_MODEL", "GROQ_API_MODEL", "LLM_MODEL")
	}

	seed, _ := cmd.Flags().GetInt("seed")
	temperature, _ := cmd.Flags().GetFloat64("temperature")
	apiKey, _ := cmd.Flags().GetString("api-key")
	apiBase, _ := cmd.Flags().GetString("api-base")
	stream, _ := cmd.Flags().GetBool("stream")
	verbose, _ := cmd.Flags().GetBool("verbose")
	chat, _ := cmd.Flags().GetBool("chat")
	chat_send, _ := cmd.Flags().GetBool("chat-send")
	systemPrompt, _ := cmd.Flags().GetString("prompt")
	debug, _ := cmd.Flags().GetBool("debug")
	maxTokens, _ := cmd.Flags().GetInt("max_tokens")
	frequencyPenalty, _ := cmd.Flags().GetFloat64("frequency_penalty")
	presencePenalty, _ := cmd.Flags().GetFloat64("presence_penalty")
	jsonMode, _ := cmd.Flags().GetBool("json")
	topP, _ := cmd.Flags().GetFloat64("top_p")
	apiParams, _ := cmd.Flags().GetString("api-params")
	jsonSchema, _ := cmd.Flags().GetString("json-schema")

	stopSequences, _ := cmd.Flags().GetString("stop")
	var stopSeqInterface interface{}
	if strings.HasPrefix(stopSequences, "[") && strings.HasSuffix(stopSequences, "]") {
		var stopSeqArray []string
		err := json.Unmarshal([]byte(stopSequences), &stopSeqArray)
		if err != nil {
			log.Fatal(err)
		}
		stopSeqInterface = stopSeqArray
	} else {
		stopSeqInterface = stopSequences
	}

	messages := make([]Message, 0)

	if len(strings.TrimSpace(systemPrompt)) > 0 {
		messages = append(messages, *NewMessage("system", systemPrompt))
	}

	var usermsg string = ""

	for _, arg := range args {
		if len(usermsg) > 0 {
			usermsg += " "
		}
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
			usermsg += " "
		}
	}

	apiKey, apiBase, err := resolveLLMApi(apiKey, apiBase)
	if err != nil {
		log.Fatal(err)
	}

	timeout := 1 * time.Second // set a 10-second timeout
	models, err := getModelList(apiKey, apiBase, timeout)
	if err != nil {
		log.Fatal(err)
	}
	if verbose {
		for _, model := range models {
			fmt.Println(model.ID, model.Meta)
		}
	}

	if debug {
		fmt.Printf("PROMPT: \"%s\"\nSYSTEM MESSAGE: \"%s\"", usermsg, systemPrompt)
		return nil
	}

	markChatStart(session, usermsg, systemPrompt, modelname, seed, temperature, apiBase, maxTokens, frequencyPenalty, presencePenalty, jsonMode, stopSeqInterface, topP, apiParams, jsonSchema)

	var extra map[string]interface{}

	apiParamsMap := map[string]interface{}{}
	if err := json.Unmarshal([]byte(apiParams), &apiParamsMap); err != nil {
		log.Fatal(err)
	}

	extra = map[string]interface{}{
		"max_tokens":        maxTokens,
		"frequency_penalty": frequencyPenalty,
		"presence_penalty":  presencePenalty,
		"top_p":             topP,
	}

	switch v := stopSeqInterface.(type) {
	case string:
		if v != "" {
			extra["stop"] = v
		}
	case []string:
		if len(v) > 0 {
			extra["stop"] = v
		}
	default:
	}

	if len(jsonSchema) > 0 {
		jsonSchemaObj := map[string]interface{}{}
		if err := json.Unmarshal([]byte(jsonSchema), &jsonSchemaObj); err != nil {
			log.Fatal(err)
		}
		extra["json_schema"] = jsonSchemaObj
	} else if jsonMode {
		extra["response_format"] = map[string]interface{}{"type": "json_object"}
	}

	for k, v := range apiParamsMap {
		extra[k] = v
	}

	llmApiFunc := func(messages []Message) (<-chan string, error) {
		filteredMessages := make([]LLMMessage, len(messages))
		for i, msg := range messages {
			filteredMessages[i] = LLMMessage{
				Role:    msg.Role,
				Content: msg.Content,
			}
		}
		return llmChat(filteredMessages, modelname, seed, temperature, nil, apiKey, apiBase, stream, extra, verbose)
	}

	llmHistoryFunc := func(msg Message) error {
		data := struct {
			ID      string  `json:"uuid"`
			SID     string  `json:"sid"`
			TS      int     `json:"ts"`
			Message Message `json:"msg"`
		}{
			ID:      msg.UUID,
			SID:     session.UUID,
			TS:      int(time.Now().Unix()),
			Message: msg,
		}

		return dumpToHistory(session, data)
	}

	if len(usermsg) == 0 || chat || chat_send {

		var initialTextareaValue = ""

		if len(usermsg) > 0 {
			initialTextareaValue = usermsg
		}

		p := tea.NewProgram(initialModel(*session, messages, llmHistoryFunc, llmApiFunc, initialTextareaValue, chat_send), // use the full size of the terminal in its "alternate screen buffer"
			tea.WithMouseCellMotion())

		if _, err := p.Run(); err != nil {
			log.Println(err)
			return err
		}

		return nil
	}

	if len(usermsg) > 0 {
		messages = append(messages, *NewMessage("user", usermsg))
	}

	ch, err := llmApiFunc(messages)

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

	// Read and log the request body
	reqBody, err := ioutil.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = ioutil.NopCloser(bytes.NewBuffer(reqBody)) // Reset req.Body

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

	// Read and log the response body
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body = ioutil.NopCloser(bytes.NewBuffer(respBody)) // Reset resp.Body
	defer resp.Body.Close()                                 // Close the response body

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

type chatTuiState struct {
	spin           bool
	streaming      bool
	spinner        spinner.Model
	viewport       viewport.Model
	textarea       textarea.Model
	llmMessages    []Message
	llmApi         func(messages []Message) (<-chan string, error)
	historyApi     func(Message) error
	session        Session
	ch             <-chan string
	err            error
	renderMarkdown bool
	viewportWidth  int
	mdPaddingWidth int
	shift          bool
	sendRightAway  bool
}

func getLastMsg(m chatTuiState) (Message, error) {
	if len(m.llmMessages) == 0 {
		return Message{}, errors.New("no messages in history")
	}
	return m.llmMessages[len(m.llmMessages)-1], nil
}

func initialModel(session Session, messages []Message, llmHistoryApi func(Message) error, llmApi func(messages []Message) (<-chan string, error), initialTextareaValue string, sendRightAway bool) chatTuiState {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Focus()

	ta.Prompt = "┃ "
	ta.CharLimit = 100000
	ta.MaxHeight = 32
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false

	vp := viewport.New(32, 12)
	vp.SetContent(`<llm chat history is empty>`)
	// vp.HighPerformanceRendering = true
	vp.MouseWheelEnabled = true
	ta.KeyMap.InsertNewline.SetEnabled(false)

	ta.SetValue(initialTextareaValue)

	if len(messages) > 0 {
		vp.SetContent(formatMessageLog(messages, true, 80, 0, "", "", true))
	}
	vp.GotoBottom()

	sp := spinner.New()

	if sendRightAway {

	}

	return chatTuiState{
		spin:           false,
		streaming:      false,
		spinner:        sp,
		textarea:       ta,
		viewport:       vp,
		llmMessages:    messages,
		llmApi:         llmApi,
		historyApi:     llmHistoryApi,
		session:        session,
		ch:             nil,
		err:            nil,
		renderMarkdown: true,
		viewportWidth:  80,
		mdPaddingWidth: 0,
		sendRightAway:  sendRightAway,
	}
}

func (m chatTuiState) Init() tea.Cmd {
	return tea.Batch(textarea.Blink)
}

func removeLastMsg(m chatTuiState) error {
	for len(m.llmMessages) > 0 {
		lastMsg, err := getLastMsg(m)
		if err != nil {
			return err
		}

		if lastMsg.Role == "assistant" {
			break
		}

		pseudoMsg := NewMessage("__sys__", fmt.Sprintf(`{"sysop": "remove_msg", "id": "%s"}`, lastMsg))
		m.historyApi(*pseudoMsg)

		m.llmMessages = m.llmMessages[:len(m.llmMessages)-1]
	}

	if len(m.llmMessages) > 0 {
		lastMsg, err := getLastMsg(m)
		if err != nil {
			return err
		}

		pseudoMsg := NewMessage("__sys__", fmt.Sprintf(`{"sysop": "remove_msg", "id": "%s"}`, lastMsg.UUID))
		m.historyApi(*pseudoMsg)

		m.llmMessages = m.llmMessages[:len(m.llmMessages)-1]
	}

	return nil
}

var markdownCache = struct {
	sync.Mutex
	cache map[string]string
}{cache: make(map[string]string)}

func formatMessageLog(msgs []Message, renderMarkdown bool, lineWidth int,
	mdPadding int, suffix string, roleFormat string, renderNewlinesInUsermsgs bool) string {

	roleFmt := "### %s:\n"
	if roleFormat != "" {
		roleFmt = roleFormat
	}

	var ret strings.Builder

	for i, msg := range msgs {
		content := strings.TrimRight(msg.Content, " \t\r\n")

		if msg.Role == "user" && renderNewlinesInUsermsgs {
			re := regexp.MustCompile(`(?m:^(  |\z)|\n)`)
			content = re.ReplaceAllStringFunc(content, func(match string) string {
				if strings.HasPrefix(match, "  ") || match == "\n" {
					return match
				}
				return "  \n"
			})
		}

		if renderMarkdown {
			key := fmt.Sprintf("%s__%s__%s", content, lineWidth, mdPadding)
			markdownCache.Lock()
			if cachedContent, ok := markdownCache.cache[key]; ok {
				markdownCache.Unlock()
				content = cachedContent
			} else {
				renderedContent := markdown.Render(content, lineWidth, mdPadding)
				markdownCache.cache[key] = string(renderedContent)
				markdownCache.Unlock()
				content = string(renderedContent)
			}
		}

		content = strings.TrimRight(content, " \t\r\n")

		sfx := ""
		if i == len(msgs)-1 && len(suffix) > 0 {
			sfx = suffix
		}

		fmt.Fprintf(&ret, roleFmt+"%s%s\n\n", strings.ToUpper(msg.Role), content, sfx)
	}

	return ret.String()
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second*1, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func sendMsg(m chatTuiState, usermsg string) (tea.Model, tea.Cmd) {
	var newmsg = *NewMessage("user", usermsg)

	m.llmMessages = append(m.llmMessages, newmsg)
	m.historyApi(newmsg)

	ch, err := m.llmApi(m.llmMessages)

	if err != nil {
		log.Println(err)
		m.err = err
		return m, nil
	}

	m.llmMessages = append(m.llmMessages, *NewMessage("assistant", ""))

	m.spin = true
	m.spinner.Spinner = spinner.Pulse
	m.spinner.Spinner.FPS = time.Second / 10
	m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("171"))

	m.ch = ch
	m.textarea.Reset()
	m.textarea.Placeholder = TEXTINPUT_PLACEHOLDER
	m.textarea.Focus()

	m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, m.spinner.View(), "", true))
	m.viewport.GotoBottom()

	return m, tea.Batch(m.spinner.Tick, readLLMResponse(m, m.ch))
}

func (m chatTuiState) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
		spCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	if m.sendRightAway {
		m.sendRightAway = false
		var usermsg = m.textarea.Value()
		_m, cmds := sendMsg(m, usermsg)
		return _m, tea.Batch(tiCmd, vpCmd, cmds)
	}

	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.Type {

		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyCtrlN: // ctrl+N
			m.llmMessages = []Message{}

			m.textarea.Reset()
			m.textarea.Placeholder = TEXTINPUT_PLACEHOLDER
			m.textarea.Focus()

			m.viewport.SetContent(`<llm chat history is empty>`)
			// m.viewport.SetContent(formatMessageLog(m.llmMessages))
			m.viewport.GotoBottom()

			return m, nil

		case tea.KeyShiftDown:
			m.shift = true
			return m, nil

		case tea.KeyShiftUp:
			m.shift = false
			return m, nil

		case tea.KeyCtrlS: // ctrl+E: copy
			if len(m.llmMessages) > 0 {
				putTextIntoClipboard(formatMessageLog(m.llmMessages, false, 0, 0, "", "", false))
			}
			return m, nil

		case tea.KeyCtrlE: // ctrl+E: copy
			// if m.shift {
			// 	putTextIntoClipboard(formatMessageLog(m.llmMessages, false, 0, 0))
			// } else {
			// 	// Copy last message
			// 	if len(m.llmMessages) > 0 {
			// 		putTextIntoClipboard(m.llmMessages[len(m.llmMessages)-1].Content)
			// 	}
			// }
			if len(m.llmMessages) > 0 {
				putTextIntoClipboard(m.llmMessages[len(m.llmMessages)-1].Content)
			}
			return m, nil

		case tea.KeyCtrlD: // ctrl+N
			removeLastMsg(m)

			m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, "", "", true))
			m.viewport.GotoBottom()

			return m, nil

		case tea.KeyEnter:
			if msg.Alt {
				m.textarea.SetValue(m.textarea.Value() + "\n")
			} else {
				var usermsg = m.textarea.Value()

				if len(strings.Trim(usermsg, " \r\t\n")) == 0 {
					return m, nil
				}

				// if len(m.llmMessages) > 0 && m.llmMessages[len(m.llmMessages)-1].Role == "user" {
				// 	// TODO customize
				// 	var lastmsg = m.llmMessages[len(m.llmMessages)-1]
				// 	var content = "# Input context:\n" + lastmsg.Content + "\n" + "# User query:\n" +

				// }

				ret, cmds := sendMsg(m, usermsg)

				return ret, tea.Batch(tiCmd, vpCmd, spCmd, cmds)
			}
		}

		// case tea.KeyBackspace:
		// 	if len(m.textarea.Value()) > 0 {
		// 		m.textarea.SetValue(m.textarea.Value()[:len(m.textarea.Value())-1])
		// 	}
		// }

	case tea.WindowSizeMsg:
		m.textarea.SetWidth(msg.Width - 2)
		m.viewport.Width = msg.Width - 2
		m.viewportWidth = msg.Width - 2
		m.viewport.Height = msg.Height - 1 - m.textarea.Height()

	case updateViewportMsg:
		content := msg.content
		streaming_done := !msg.streaming

		if m.spin {
			m.spin = false
			m.streaming = true
			m.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
		}

		if streaming_done {
			m.streaming = false
			return m, nil
		}

		if len(m.llmMessages) > 0 && m.llmMessages[len(m.llmMessages)-1].Role == "assistant" {
			m.llmMessages[len(m.llmMessages)-1].Content += content
		} else {
			m.llmMessages = append(m.llmMessages, *NewMessage("assistant", content))
			m.spin = false
		}

		m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, "", "", true))
		m.viewport.GotoBottom()

		return m, tea.Batch(tiCmd, vpCmd, spCmd, readLLMResponse(m, m.ch))

	default:
		// fmt.Println(msg)
	}

	if m.spin || m.streaming {
		m.spinner, spCmd = m.spinner.Update(msg)
		return m, tea.Batch(tiCmd, vpCmd, spCmd)
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m chatTuiState) View() string {

	if m.spin || m.streaming {
		m.viewport.SetContent(formatMessageLog(m.llmMessages, m.renderMarkdown, m.viewportWidth, m.mdPaddingWidth, m.spinner.View(), "", true))
	}

	return fmt.Sprintf(
		"%s\n%s",
		m.viewport.View(),
		m.textarea.View(),
	) + "\n"
}

func readLLMResponse(m chatTuiState, ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		for content := range ch {
			return updateViewportMsg{content: content, streaming: true}
		}
		var lastMsg, err = getLastMsg(m)
		if err == nil {
			m.historyApi(lastMsg)
		}
		return updateViewportMsg{content: "", streaming: false}
	}
}

type updateViewportMsg struct {
	streaming bool
	content   string
}
