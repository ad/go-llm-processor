package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type SSEEvent struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	Timestamp int64           `json:"timestamp"`
	ID        string          `json:"id,omitempty"`
}

type TaskAvailableData struct {
	TaskID                  string `json:"taskId"`
	Priority                int    `json:"priority"`
	RetryCount              int    `json:"retryCount"`
	EstimatedProcessingTime int    `json:"estimatedProcessingTime,omitempty"`
}

type ProcessorMetricsData struct {
	TotalProcessors   int `json:"totalProcessors"`
	ActiveTasks       int `json:"activeTasks"`
	PendingTasks      int `json:"pendingTasks"`
	AvgProcessingTime int `json:"avgProcessingTime"`
}

type SSEConfig struct {
	Enabled              bool          `env:"SSE_ENABLED" envDefault:"true"`
	Endpoint             string        `env:"SSE_ENDPOINT" envDefault:"/api/internal/task-stream"`
	ReconnectInterval    time.Duration `env:"SSE_RECONNECT_INTERVAL" envDefault:"5s"`
	MaxReconnectAttempts int           `env:"SSE_MAX_RECONNECT_ATTEMPTS" envDefault:"10"`
	HeartbeatTimeout     time.Duration `env:"SSE_HEARTBEAT_TIMEOUT" envDefault:"60s"`
	HeartbeatInterval    time.Duration `env:"SSE_HEARTBEAT_INTERVAL" envDefault:"30s"`
	MaxDuration          time.Duration `env:"SSE_MAX_DURATION" envDefault:"1h"`
}

type SSEClient struct {
	client       *http.Client
	baseURL      string
	token        string
	config       SSEConfig
	processorID  string
	taskChan     chan TaskAvailableData
	errorChan    chan error
	stopChan     chan struct{}
	reconnecting bool
	stopped      bool
	mutex        sync.Mutex
}

func NewSSEClient(baseURL, token, processorID string, config SSEConfig) *SSEClient {
	return &SSEClient{
		client: &http.Client{
			Timeout: 0, // No timeout for SSE connections
		},
		baseURL:     baseURL,
		token:       token,
		config:      config,
		processorID: processorID,
		taskChan:    make(chan TaskAvailableData, 100),
		errorChan:   make(chan error, 10),
		stopChan:    make(chan struct{}),
	}
}

func (c *SSEClient) Start(ctx context.Context) {
	if !c.config.Enabled {
		log.Println("SSE client disabled, skipping")
		return
	}

	log.Printf("Starting SSE client for processor %s\n", c.processorID)
	go c.connectLoop(ctx)
}

func (c *SSEClient) Stop() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.stopped {
		return
	}

	log.Println("Stopping SSE client")
	c.stopped = true
	close(c.stopChan)
}

func (c *SSEClient) GetTaskChannel() <-chan TaskAvailableData {
	return c.taskChan
}

func (c *SSEClient) GetErrorChannel() <-chan error {
	return c.errorChan
}

func (c *SSEClient) connectLoop(ctx context.Context) {
	attempts := 0

	for {
		select {
		case <-ctx.Done():
			log.Println("SSE context cancelled")
			return
		case <-c.stopChan:
			log.Println("SSE stop signal received")
			return
		default:
		}

		if attempts >= c.config.MaxReconnectAttempts {
			log.Printf("Max reconnect attempts (%d) reached, giving up\n", c.config.MaxReconnectAttempts)
			c.errorChan <- fmt.Errorf("max reconnect attempts reached")
			return
		}

		log.Printf("SSE connection attempt %d/%d\n", attempts+1, c.config.MaxReconnectAttempts)

		err := c.connect(ctx)
		if err != nil {
			log.Printf("SSE connection failed: %v\n", err)
			attempts++

			select {
			case c.errorChan <- err:
			default:
			}

			// Exponential backoff
			backoff := c.config.ReconnectInterval * time.Duration(1<<uint(attempts-1))
			if backoff > time.Minute {
				backoff = time.Minute
			}

			log.Printf("Reconnecting in %v\n", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			case <-c.stopChan:
				return
			}
		} else {
			attempts = 0 // Reset attempts on successful connection
		}
	}
}

func (c *SSEClient) connect(ctx context.Context) error {
	url := fmt.Sprintf("%s%s?heartbeat=%d&maxDuration=%d&processor_id=%s",
		c.baseURL, c.config.Endpoint,
		int(c.config.HeartbeatInterval.Milliseconds()),
		int(c.config.MaxDuration.Milliseconds()),
		c.processorID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	log.Printf("Connecting to SSE endpoint: %s with token %s\n", url, c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	log.Printf("SSE connection established for processor %s\n", c.processorID)
	return c.readEvents(ctx, resp.Body)
}

func (c *SSEClient) readEvents(ctx context.Context, reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	var event SSEEvent
	var lines []string
	lastHeartbeat := time.Now()

	// Heartbeat checker
	heartbeatTicker := time.NewTicker(c.config.HeartbeatTimeout)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.stopChan:
			return nil
		case <-heartbeatTicker.C:
			if time.Since(lastHeartbeat) > c.config.HeartbeatTimeout {
				return fmt.Errorf("heartbeat timeout exceeded")
			}
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("reading stream: %w", err)
			}
			return fmt.Errorf("stream ended unexpectedly")
		}

		line := scanner.Text()

		// Empty line indicates end of event
		if line == "" {
			if len(lines) > 0 {
				if err := c.parseEvent(lines, &event); err != nil {
					log.Printf("Error parsing event: %v\n", err)
				} else {
					if err := c.handleEvent(event); err != nil {
						log.Printf("Error handling event: %v\n", err)
					}
					if event.Type == "heartbeat" {
						lastHeartbeat = time.Now()
					}
				}
				lines = lines[:0]
			}
			continue
		}

		lines = append(lines, line)
	}
}

func (c *SSEClient) parseEvent(lines []string, event *SSEEvent) error {
	*event = SSEEvent{}

	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			event.Type = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataStr := strings.TrimPrefix(line, "data: ")
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
				return fmt.Errorf("parsing event data: %w", err)
			}

			// Extract timestamp if present
			if ts, ok := data["timestamp"].(float64); ok {
				event.Timestamp = int64(ts)
			}

			// Store raw data
			event.Data = json.RawMessage(dataStr)
		} else if strings.HasPrefix(line, "id: ") {
			event.ID = strings.TrimPrefix(line, "id: ")
		}
	}

	// Если не найден event.Type, вернуть специальную ошибку
	if event.Type == "" {
		return nil //fmt.Errorf("empty event type, skip event")
	}

	return nil
}

func (c *SSEClient) handleEvent(event SSEEvent) error {
	if event.Type == "" {
		return nil
	}

	log.Printf("Received SSE event: type=%s, timestamp=%d\n", event.Type, event.Timestamp)

	switch event.Type {
	case "task_available":
		var taskData TaskAvailableData
		if err := json.Unmarshal(event.Data, &taskData); err != nil {
			return fmt.Errorf("parsing task_available data: %w", err)
		}

		log.Printf("New task available: %s (priority=%d, retryCount=%d)\n",
			taskData.TaskID, taskData.Priority, taskData.RetryCount)

		select {
		case c.taskChan <- taskData:
		default:
			log.Printf("Task channel full, dropping task notification: %s\n", taskData.TaskID)
		}

	case "heartbeat":
		log.Printf("Received heartbeat from server\n")

	case "processor_metrics":
		var metrics ProcessorMetricsData
		if err := json.Unmarshal(event.Data, &metrics); err != nil {
			log.Printf("Error parsing processor metrics: %v\n", err)
		} else {
			log.Printf("Processor metrics: total=%d, active=%d, pending=%d, avgTime=%d\n",
				metrics.TotalProcessors, metrics.ActiveTasks, metrics.PendingTasks, metrics.AvgProcessingTime)
		}

	case "error":
		var errorData map[string]interface{}
		if err := json.Unmarshal(event.Data, &errorData); err != nil {
			return fmt.Errorf("parsing error data: %w", err)
		}

		if errorMsg, ok := errorData["error"].(string); ok {
			return fmt.Errorf("server error: %s", errorMsg)
		}

	default:
		// log.Printf("Unknown SSE event type: %s\n", event.Type)
	}

	return nil
}
