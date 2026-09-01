package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Provider is the LLM backend type.
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderOllama Provider = "ollama"
	ProviderOpenAI Provider = "openai"
	ProviderNone   Provider = "none"
)

// Config holds LLM provider settings.
type Config struct {
	Provider Provider
	APIKeyEnv string // env var name (never store keys directly)
	Model    string
	Endpoint string // for ollama/custom endpoints
}

// Client provides a unified LLM interface.
type Client struct {
	cfg Config
}

// New creates a new LLM client from config.
func New(cfg Config) *Client {
	return &Client{cfg: cfg}
}

// Available returns true if the configured provider can be used.
func (c *Client) Available() bool {
	if c.cfg.Provider == ProviderNone {
		return false
	}
	if c.cfg.Provider == ProviderOllama {
		// Check if Ollama is running
		endpoint := c.cfg.Endpoint
		if endpoint == "" {
			endpoint = "http://localhost:11434"
		}
		resp, err := http.Get(endpoint + "/api/version")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	// Claude or OpenAI — need API key
	return c.getAPIKey() != ""
}

// Complete sends a prompt and returns the response text.
func (c *Client) Complete(systemPrompt, userPrompt string) (string, error) {
	switch c.cfg.Provider {
	case ProviderClaude:
		return c.completeClaude(systemPrompt, userPrompt)
	case ProviderOllama:
		return c.completeOllama(systemPrompt, userPrompt)
	case ProviderOpenAI:
		return c.completeOpenAI(systemPrompt, userPrompt)
	case ProviderNone:
		return "", fmt.Errorf("no LLM provider configured (set [ai] provider in config)")
	default:
		return "", fmt.Errorf("unknown provider: %s", c.cfg.Provider)
	}
}

// GapAnalysis generates "what to do next" from session context.
func (c *Client) GapAnalysis(context string) (string, error) {
	system := `You are a developer productivity assistant. Given the current project state, generate 2-3 concise prioritized next actions. Focus on: failing tests, pending migrations, stale dependencies, unfinished features. Output plain text, one action per line, most urgent first. No preamble.`
	return c.Complete(system, context)
}

// IntentClassify classifies what the user is working on from command history.
func (c *Client) IntentClassify(history string) (string, error) {
	system := `Classify the developer's current work intent from their command history. Return a single short phrase (3-8 words) describing what they're doing. Examples: "debugging JWT auth middleware", "shipping crash recovery feature", "setting up CI pipeline". No preamble, just the phrase.`
	return c.Complete(system, "Recent commands:\n"+history)
}

// Summarize generates a work recap from session data.
func (c *Client) Summarize(sessionData string) (string, error) {
	system := `You are a concise developer productivity assistant. Summarize the work session in 2-4 sentences. Mention: what was accomplished, what's in progress, and any blockers. Use second person ("you"). No preamble.`
	return c.Complete(system, sessionData)
}

// --- Provider implementations ---

func (c *Client) completeClaude(system, user string) (string, error) {
	key := c.getAPIKey()
	if key == "" {
		return "", fmt.Errorf("API key not found in env var %s", c.cfg.APIKeyEnv)
	}

	model := c.cfg.Model
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	body := map[string]interface{}{
		"model":      model,
		"max_tokens": 1024,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}

	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpDo(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	if result.Error.Message != "" {
		return "", fmt.Errorf("claude: %s", result.Error.Message)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude")
	}
	return result.Content[0].Text, nil
}

func (c *Client) completeOllama(system, user string) (string, error) {
	endpoint := c.cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	model := c.cfg.Model
	if model == "" {
		model = "llama3.2"
	}

	body := map[string]interface{}{
		"model":  model,
		"system": system,
		"prompt": user,
		"stream": false,
	}

	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", endpoint+"/api/generate", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpDo(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding ollama response: %w", err)
	}
	return strings.TrimSpace(result.Response), nil
}

func (c *Client) completeOpenAI(system, user string) (string, error) {
	key := c.getAPIKey()
	if key == "" {
		return "", fmt.Errorf("API key not found in env var %s", c.cfg.APIKeyEnv)
	}

	model := c.cfg.Model
	if model == "" {
		model = "gpt-4o-mini"
	}

	endpoint := c.cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}

	body := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"max_tokens": 1024,
	}

	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", endpoint+"/chat/completions", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.httpDo(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	respBody, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decoding openai response: %w", err)
	}
	if result.Error.Message != "" {
		return "", fmt.Errorf("openai: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty response from OpenAI")
	}
	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

func (c *Client) getAPIKey() string {
	envVar := c.cfg.APIKeyEnv
	if envVar == "" {
		switch c.cfg.Provider {
		case ProviderClaude:
			envVar = "ANTHROPIC_API_KEY"
		case ProviderOpenAI:
			envVar = "OPENAI_API_KEY"
		}
	}
	return os.Getenv(envVar)
}

func (c *Client) httpDo(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}
	return resp, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
