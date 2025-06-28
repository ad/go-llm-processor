package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ad/llm-proxy/processor/pkg/ollama"
)

type Client struct {
	baseURL        string
	httpClient     *http.Client
	internalAPIKey string
}

type Task struct {
	ID           string  `json:"id"`
	ProductData  string  `json:"product_data"`
	Priority     int     `json:"priority"`
	RetryCount   int     `json:"retry_count"`
	OllamaParams *string `json:"ollama_params,omitempty"` // JSON string
}

// ParseOllamaParams extracts and parses ollama parameters from task
func (t *Task) ParseOllamaParams() (*ollama.OllamaParams, error) {
	if t.OllamaParams == nil || *t.OllamaParams == "" {
		return nil, nil
	}

	var params ollama.OllamaParams
	if err := json.Unmarshal([]byte(*t.OllamaParams), &params); err != nil {
		return nil, fmt.Errorf("failed to parse ollama_params: %w", err)
	}

	return &params, nil
}

type ClaimRequest struct {
	TaskID      string `json:"taskId"`
	ProcessorID string `json:"processor_id"`
	TimeoutMs   int    `json:"timeout_ms,omitempty"`
}

type HeartbeatRequest struct {
	TaskID      string `json:"taskId"`
	ProcessorID string `json:"processor_id"`
}

type ProcessorHeartbeatRequest struct {
	ProcessorID string   `json:"processor_id"`
	CPUUsage    *float64 `json:"cpu_usage,omitempty"`
	MemoryUsage *float64 `json:"memory_usage,omitempty"`
	QueueSize   *int     `json:"queue_size,omitempty"`
}

type TasksResponse struct {
	Tasks []Task `json:"tasks"`
}

type CompleteRequest struct {
	ProcessorID string `json:"processor_id"`
	Results     []struct {
		TaskID       string  `json:"taskId"`
		Status       string  `json:"status"`
		Result       *string `json:"result,omitempty"`
		ErrorMessage *string `json:"error_message,omitempty"`
	} `json:"results"`
}

type BatchClaimRequest struct {
	ProcessorID string `json:"processor_id"`
	BatchSize   int    `json:"batch_size"`
	TimeoutMs   int    `json:"timeout_ms,omitempty"`
}

type BatchClaimResponse struct {
	Success bool   `json:"success"`
	Tasks   []Task `json:"tasks"`
	Count   int    `json:"claimed_count"`
}

func NewClient(baseURL string, internalAPIKey string) *Client {
	return &Client{
		baseURL:        baseURL,
		internalAPIKey: internalAPIKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// addAuthHeaders добавляет заголовок авторизации к запросу
func (c *Client) addAuthHeaders(req *http.Request) {
	if c.internalAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.internalAPIKey)
	}
	req.Header.Set("Content-Type", "application/json")
}

func (c *Client) GetPendingTasks(ctx context.Context) ([]Task, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/internal/tasks", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	var result TasksResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Tasks, nil
}

func (c *Client) CompleteTask(ctx context.Context, taskID, processorID, status, result, errorMessage string) error {
	var resultPtr *string
	var errorPtr *string

	if result != "" {
		resultPtr = &result
	}
	if errorMessage != "" {
		errorPtr = &errorMessage
	}

	// Use the format expected by Go worker (single task completion)
	req := map[string]interface{}{
		"taskId":        taskID,
		"status":        status,
		"result":        resultPtr,
		"error_message": errorPtr,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/complete", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) ClaimTask(ctx context.Context, taskID, processorID string, timeoutMs int) error {
	req := ClaimRequest{
		TaskID:      taskID,
		ProcessorID: processorID,
		TimeoutMs:   timeoutMs,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/claim", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("task already claimed")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) SendHeartbeat(ctx context.Context, taskID, processorID string) error {
	req := HeartbeatRequest{
		TaskID:      taskID,
		ProcessorID: processorID,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/heartbeat", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) TriggerCleanup(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/cleanup", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) ClaimTasksBatch(ctx context.Context, processorID string, batchSize int, timeoutMs int) ([]Task, error) {
	req := BatchClaimRequest{
		ProcessorID: processorID,
		BatchSize:   batchSize,
		TimeoutMs:   timeoutMs,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/claim", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	var result BatchClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Tasks, nil
}

func (c *Client) ReleaseTask(ctx context.Context, taskID string) error {
	reqBody := fmt.Sprintf(`{"taskId": "%s"}`, taskID)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/release", bytes.NewReader([]byte(reqBody)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *Client) SendProcessorHeartbeat(ctx context.Context, processorID string, cpuUsage, memoryUsage *float64, queueSize *int) error {
	req := ProcessorHeartbeatRequest{
		ProcessorID: processorID,
		CPUUsage:    cpuUsage,
		MemoryUsage: memoryUsage,
		QueueSize:   queueSize,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/processor-heartbeat", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.addAuthHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// RequeueTask отправляет запрос на возврат задачи в пул (requeue) в manager
func (c *Client) RequeueTask(ctx context.Context, taskID, processorID, reason string) error {
	request := map[string]interface{}{
		"taskId":       taskID,
		"processor_id": processorID,
		"reason":       reason,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal requeue request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/internal/requeue", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create requeue request: %w", err)
	}
	c.addAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do requeue request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("requeue error %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
