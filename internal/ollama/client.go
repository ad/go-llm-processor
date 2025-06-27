package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	modelCache map[string]bool
	cacheMutex sync.RWMutex
	sf         singleflight.Group
}

type OllamaParams struct {
	Model         string   `json:"model,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	MaxTokens     *int     `json:"max_tokens,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	TopK          *int     `json:"top_k,omitempty"`
	RepeatPenalty *float64 `json:"repeat_penalty,omitempty"`
	Seed          *int     `json:"seed,omitempty"`
	Stop          []string `json:"stop,omitempty"`
}

type GenerateRequest struct {
	Model         string   `json:"model"`
	Prompt        string   `json:"prompt"`
	Stream        bool     `json:"stream"`
	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	TopK          *int     `json:"top_k,omitempty"`
	RepeatPenalty *float64 `json:"repeat_penalty,omitempty"`
	Seed          *int     `json:"seed,omitempty"`
	Stop          []string `json:"stop,omitempty"`
	NumPredict    *int     `json:"num_predict,omitempty"` // max_tokens equivalent
}

type GenerateResponse struct {
	Model     string    `json:"model"`
	Response  string    `json:"response"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

// --- Новый тип для chat/completions ---
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model         string        `json:"model"`
	Messages      []ChatMessage `json:"messages"`
	Temperature   *float64      `json:"temperature,omitempty"`
	TopP          *float64      `json:"top_p,omitempty"`
	TopK          *int          `json:"top_k,omitempty"`
	RepeatPenalty *float64      `json:"repeat_penalty,omitempty"`
	Seed          *int          `json:"seed,omitempty"`
	Stop          []string      `json:"stop,omitempty"`
	MaxTokens     *int          `json:"max_tokens,omitempty"`
	Stream        bool          `json:"stream,omitempty"`
}

type ChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		modelCache: make(map[string]bool),
	}
}

func (c *Client) Generate(ctx context.Context, model, prompt string) (string, error) {
	return c.GenerateWithParams(ctx, model, prompt, nil)
}

func (c *Client) GenerateWithParams(ctx context.Context, model, userPrompt string, params *OllamaParams) (string, error) {
	var messages []ChatMessage
	if params != nil && params.Prompt != "" {
		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: params.Prompt,
		})
	}
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: userPrompt,
	})

	// Собираем параметры
	req := ChatCompletionRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
	}
	if params != nil {
		if params.Temperature != nil {
			req.Temperature = params.Temperature
		}
		if params.MaxTokens != nil {
			req.MaxTokens = params.MaxTokens
		}
		if params.TopP != nil {
			req.TopP = params.TopP
		}
		if params.TopK != nil {
			req.TopK = params.TopK
		}
		if params.RepeatPenalty != nil {
			req.RepeatPenalty = params.RepeatPenalty
		}
		if params.Seed != nil {
			req.Seed = params.Seed
		}
		if params.Stop != nil {
			req.Stop = params.Stop
		}
		if params.Model != "" {
			req.Model = params.Model
		}
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama error %d: %s", resp.StatusCode, string(body))
	}

	var result ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama health check failed: %d", resp.StatusCode)
	}

	return nil
}

// Проверка кэша
func (c *Client) isModelCached(modelName string) bool {
	c.cacheMutex.RLock()
	defer c.cacheMutex.RUnlock()
	return c.modelCache[modelName]
}

func (c *Client) cacheModel(modelName string, available bool) {
	c.cacheMutex.Lock()
	defer c.cacheMutex.Unlock()
	c.modelCache[modelName] = available
}

// Проверка наличия модели через Ollama API
func (c *Client) checkModelAvailability(modelName string) error {
	resp, err := c.httpClient.Get(c.baseURL + "/api/tags")
	if err != nil {
		return fmt.Errorf("failed to get models list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error when getting models: status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read models response: %w", err)
	}

	var modelsResp struct {
		Models []struct {
			Name string `json:"name"`
		}
	}
	if err := json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		return fmt.Errorf("failed to decode models response: %w", err)
	}

	for _, model := range modelsResp.Models {
		if strings.Contains(model.Name, modelName) || model.Name == modelName {
			c.cacheModel(modelName, true)
			return nil
		}
	}
	return fmt.Errorf("model %s not found in available models", modelName)
}

// Скачивание модели через Ollama API
func (c *Client) pullModel(modelName string) error {
	log.Printf("Скачивание модели: %s\n", modelName)
	pullReq := struct {
		Name   string `json:"name"`
		Stream bool   `json:"stream"`
	}{Name: modelName, Stream: true}

	jsonData, err := json.Marshal(pullReq)
	if err != nil {
		return fmt.Errorf("failed to marshal pull request: %w", err)
	}

	resp, err := c.httpClient.Post(c.baseURL+"/api/pull", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to send pull request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error when pulling model: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	decoder := json.NewDecoder(resp.Body)
	var lastStatus string
	var lastProgress float64

	for {
		var pullResp struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			Total     int    `json:"total"`
			Completed int    `json:"completed"`
		}
		if err := decoder.Decode(&pullResp); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode pull response: %w", err)
		}
		if pullResp.Total > 0 && pullResp.Completed > 0 {
			percentage := float64(pullResp.Completed) / float64(pullResp.Total) * 100
			if percentage-lastProgress >= 10.0 {
				fmt.Printf("Скачивание %s: %.0f%%\n", modelName, percentage)
				lastProgress = percentage
			}
		} else if pullResp.Status != lastStatus && pullResp.Status != "" {
			fmt.Printf("Модель %s: %s\n", modelName, pullResp.Status)
			lastStatus = pullResp.Status
		}
	}
	log.Printf("Модель %s скачана успешно\n", modelName)
	return nil
}

// Гарантирует, что модель доступна (тихо, без лишних логов)
func (c *Client) EnsureModelAvailable(modelName string) error {
	if c.isModelCached(modelName) {
		return nil
	}
	_, err, _ := c.sf.Do(modelName, func() (interface{}, error) {
		if err := c.checkModelAvailability(modelName); err == nil {
			return nil, nil
		}
		log.Printf("Модель %s не найдена, начинаем скачивание...\n", modelName)
		if err := c.pullModel(modelName); err != nil {
			return nil, fmt.Errorf("failed to download model %s: %w", modelName, err)
		}
		if err := c.checkModelAvailability(modelName); err != nil {
			return nil, fmt.Errorf("model %s still not available after download: %w", modelName, err)
		}
		return nil, nil
	})
	return err
}
