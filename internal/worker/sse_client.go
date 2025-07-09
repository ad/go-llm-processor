package worker

import (
	"bufio"
	"context"
	"crypto/tls"
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
	TaskID                  string  `json:"taskId"`
	Priority                int     `json:"priority"`
	ProductData             string  `json:"productData,omitempty"`
	OllamaParams            *string `json:"ollamaParams,omitempty"`
	RetryCount              int     `json:"retryCount"`
	EstimatedProcessingTime int     `json:"estimatedProcessingTime,omitempty"`
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
	HeartbeatTimeout     time.Duration `env:"SSE_HEARTBEAT_TIMEOUT" envDefault:"90s"`  // Увеличиваем до 90 секунд
	HeartbeatInterval    time.Duration `env:"SSE_HEARTBEAT_INTERVAL" envDefault:"30s"` // Уменьшаем до 30 секунд для более частых heartbeat'ов
	MaxDuration          time.Duration `env:"SSE_MAX_DURATION" envDefault:"50m"`       // Проактивно переподключаемся каждые 50 минут
}

type SSEClient struct {
	client           *http.Client
	baseURL          string
	token            string
	config           SSEConfig
	processorID      string
	taskChan         chan TaskAvailableData
	errorChan        chan error
	stopChan         chan struct{}
	stopped          bool
	mutex            sync.Mutex
	http2Errors      int
	totalErrors      int
	lastErrorTime    time.Time
	forceHTTP2Accept bool // Флаг для принятия работы через HTTP/2
}

func NewSSEClient(baseURL, token, processorID string, config SSEConfig) *SSEClient {
	// Создаем HTTP клиент с агрессивными настройками для принудительного HTTP/1.1
	transport := &http.Transport{
		ForceAttemptHTTP2:     false, // Принудительно отключаем HTTP/2
		DisableKeepAlives:     false,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Дополнительные настройки для HTTP/1.1
		DisableCompression: false,
		MaxConnsPerHost:    5,
		// Принудительно отключаем все TLS расширения, которые могут вызвать HTTP/2
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}

	return &SSEClient{
		client: &http.Client{
			Timeout:   0, // No timeout for SSE connections
			Transport: transport,
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
			return
		case <-c.stopChan:
			return
		default:
		}

		c.mutex.Lock()
		if c.stopped {
			c.mutex.Unlock()
			return
		}
		c.mutex.Unlock()

		err := c.connect(ctx)
		if err != nil {
			c.totalErrors++
			c.lastErrorTime = time.Now()

			// Проверяем тип ошибки для разных стратегий обработки
			if c.isHTTP2Error(err) {
				log.Printf("SSE connection failed: %v (total errors: %d, HTTP/2 errors: %d)", err, c.totalErrors, c.http2Errors)
				c.http2Errors++

				// После 5 HTTP/2 ошибок пробуем принудительно принять HTTP/2
				if c.http2Errors >= 5 && !c.forceHTTP2Accept {
					log.Println("Too many HTTP/2 errors, trying to accept HTTP/2 with optimized settings")
					c.acceptHTTP2()
				} else if c.http2Errors >= 1 {
					log.Println("Switching to HTTP/1.1 due to HTTP/2 error")
					c.switchToHTTP1()
				}
			} else if c.isTimeoutError(err) {
				// log.Printf("Detected server timeout (%s), this is expected behavior", err.Error())
				// Для таймаутов не увеличиваем backoff агрессивно
				if attempts > 3 {
					attempts = 1 // Ограничиваем backoff для таймаутов
				}
			} else if c.isProactiveReconnection(err) {
				log.Println("Proactive reconnection completed")
				attempts = 0 // Сбрасываем attempts для проактивных переподключений
			}

			attempts++
			if attempts >= c.config.MaxReconnectAttempts {
				log.Printf("Max reconnection attempts reached (%d), stopping SSE client", attempts)
				select {
				case c.errorChan <- fmt.Errorf("max reconnection attempts reached: %w", err):
				default:
				}
				return
			}

			// Уменьшаем backoff для known issues (таймауты, проактивные переподключения)
			backoff := time.Duration(attempts) * c.config.ReconnectInterval
			if c.isTimeoutError(err) || c.isProactiveReconnection(err) {
				backoff = c.config.ReconnectInterval // Минимальный backoff для ожидаемых переподключений
			}
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}

			log.Printf("Reconnecting in %v (attempt %d/%d)", backoff, attempts, c.config.MaxReconnectAttempts)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			case <-c.stopChan:
				return
			}
		} else {
			attempts = 0
			c.http2Errors = 0 // Reset HTTP/2 errors on successful connection
		}
	}
}

func (c *SSEClient) isHTTP2Error(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "stream error") ||
		strings.Contains(errStr, "INTERNAL_ERROR") ||
		strings.Contains(errStr, "http2") ||
		strings.Contains(errStr, "HTTP/2")
}

func (c *SSEClient) isTimeoutError(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "unexpected EOF") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "broken pipe")
}

func (c *SSEClient) isProactiveReconnection(err error) bool {
	return strings.Contains(err.Error(), "proactive reconnection")
}

func (c *SSEClient) switchToHTTP1() {
	log.Println("Configuring HTTP/1.1 transport with aggressive settings")

	transport := &http.Transport{
		ForceAttemptHTTP2:     false,
		DisableKeepAlives:     false,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
		MaxConnsPerHost:       5,
		// Агрессивно отключаем все TLS протоколы, которые могут вызвать HTTP/2
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}

	// Закрываем все существующие соединения
	if oldTransport, ok := c.client.Transport.(*http.Transport); ok {
		oldTransport.CloseIdleConnections()
	}

	c.client.Transport = transport
	log.Println("Aggressive HTTP/1.1 transport configured and old connections closed")
}

func (c *SSEClient) acceptHTTP2() {
	log.Println("Accepting HTTP/2 with optimized settings for SSE stability")

	c.forceHTTP2Accept = true

	transport := &http.Transport{
		ForceAttemptHTTP2:     true, // Принимаем HTTP/2
		DisableKeepAlives:     false,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
		MaxConnsPerHost:       5,
		// Настройки для более стабильного HTTP/2
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
	}

	// Закрываем все существующие соединения
	if oldTransport, ok := c.client.Transport.(*http.Transport); ok {
		oldTransport.CloseIdleConnections()
	}

	c.client.Transport = transport
	log.Println("Optimized HTTP/2 transport configured and old connections closed")
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
	req.Header.Set("User-Agent", "go-llm-processor/1.0")
	req.Header.Set("HTTP2-Settings", "")       // Явно отключаем HTTP/2 upgrade
	req.Header.Set("Upgrade", "")              // Отключаем любые upgrades
	req.Header.Set("Connection", "keep-alive") // Заголовок Connection только один раз

	// Принудительно устанавливаем HTTP/1.1
	req.Proto = "HTTP/1.1"
	req.ProtoMajor = 1
	req.ProtoMinor = 1

	// log.Printf("Connecting to SSE endpoint: %s (heartbeat: %v, maxDuration: %v)", url, c.config.HeartbeatInterval, c.config.MaxDuration)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// log.Printf("SSE connection established for processor %s via %s", c.processorID, resp.Proto)

	// Проверяем, игнорирует ли сервер наши HTTP/1.1 настройки
	if resp.Proto == "HTTP/2.0" {
		log.Printf("WARNING: Server forced HTTP/2.0 despite HTTP/1.1 request - server/proxy ignoring client preferences")
		log.Printf("WARNING: This may cause stream errors due to HTTP/2 instability with SSE")
	} else {
		// log.Printf("SUCCESS: Connection established using requested protocol %s", resp.Proto)
	}

	// Добавляем отдельный канал для успешного подключения
	connectionEstablished := make(chan struct{})
	go func() {
		// Сигнализируем об успешном подключении после небольшой задержки
		time.Sleep(100 * time.Millisecond)
		select {
		case connectionEstablished <- struct{}{}:
		default:
		}
	}()

	// Запускаем чтение событий в отдельной горутине
	errChan := make(chan error, 1)
	go func() {
		errChan <- c.readEvents(ctx, resp.Body)
	}()

	// Добавляем проактивный таймер для переподключения
	maxDurationTimer := time.NewTimer(c.config.MaxDuration)
	defer maxDurationTimer.Stop()

	// Ждем либо успешного подключения, либо ошибки, либо проактивного переподключения
	select {
	case <-connectionEstablished:
		// Соединение успешно установлено, теперь ждем ошибки или таймера
		select {
		case err := <-errChan:
			return err
		case <-maxDurationTimer.C:
			log.Printf("Proactive reconnection after %v to avoid server timeout", c.config.MaxDuration)
			return fmt.Errorf("proactive reconnection")
		case <-ctx.Done():
			return ctx.Err()
		case <-c.stopChan:
			return nil
		}
	case err := <-errChan:
		// Ошибка произошла до того, как соединение стабилизировалось
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stopChan:
		return nil
	}
}

func (c *SSEClient) readEvents(ctx context.Context, reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	var event SSEEvent
	var lines []string
	lastHeartbeat := time.Now()

	// Увеличиваем размер буфера для более стабильной работы с HTTP/2
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Heartbeat checker
	heartbeatTicker := time.NewTicker(c.config.HeartbeatTimeout)
	defer heartbeatTicker.Stop()

	// log.Printf("SSE readEvents: started reading events for processor %s", c.processorID)

	for {
		select {
		case <-ctx.Done():
			log.Printf("SSE readEvents: context cancelled: %v", ctx.Err())
			return ctx.Err()
		case <-c.stopChan:
			log.Println("SSE readEvents: stop signal received")
			return nil
		case <-heartbeatTicker.C:
			if time.Since(lastHeartbeat) > c.config.HeartbeatTimeout {
				log.Printf("SSE readEvents: heartbeat timeout exceeded (last: %v ago)", time.Since(lastHeartbeat))
				return fmt.Errorf("heartbeat timeout exceeded")
			}
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				// log.Printf("SSE readEvents: scanner error: %v (type: %T)", err, err)
				return fmt.Errorf("reading stream: %w", err)
			}
			log.Println("SSE readEvents: stream ended unexpectedly (scanner returned false)")
			return fmt.Errorf("stream ended unexpectedly")
		}

		line := scanner.Text()

		// Empty line indicates end of event
		if line == "" {
			if len(lines) > 0 {
				if err := c.parseEvent(lines, &event); err != nil {
					log.Printf("Error parsing event: %v", err)
				} else {
					lastHeartbeat = time.Now() // Update heartbeat on any event
					if err := c.handleEvent(event); err != nil {
						log.Printf("Error handling event: %v", err)
					}
				}
				lines = nil
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

			// Извлекаем type из JSON, если нет отдельного event: ...
			if t, ok := data["type"].(string); ok && event.Type == "" {
				event.Type = t
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
		// log.Printf("Received empty SSE event type, skipping %v\n", event)
		return nil
	}

	// log.Printf("Received SSE event: type=%s, timestamp=%d\n", event.Type, event.Timestamp)

	switch event.Type {
	case "task_available":
		// log.Printf("task_available raw event.Data: %s", string(event.Data))
		// Сначала распарсим event.Data как map, затем возьмём поле data и его распарсим в TaskAvailableData
		var wrapper struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(event.Data, &wrapper); err != nil {
			log.Printf("task_available: error parsing wrapper: %v, data: %s", err, string(event.Data))
			return fmt.Errorf("parsing task_available wrapper: %w", err)
		}
		var taskData TaskAvailableData
		if err := json.Unmarshal(wrapper.Data, &taskData); err != nil {
			log.Printf("task_available: error parsing inner data: %v, data: %s", err, string(wrapper.Data))
			return fmt.Errorf("parsing task_available data: %w", err)
		}
		// log.Printf("task_available parsed: taskId=%s, priority=%d, retryCount=%d, productData=%s, ollamaParams=%v", taskData.TaskID, taskData.Priority, taskData.RetryCount, taskData.ProductData, taskData.OllamaParams)
		// log.Printf("task_available: sending to taskChan: %s", taskData.TaskID)
		select {
		case c.taskChan <- taskData:
			// log.Printf("task_available: successfully sent to taskChan: %s", taskData.TaskID)
		default:
			log.Printf("Task channel full, dropping task notification: %s", taskData.TaskID)
		}

	case "heartbeat":
		// log.Printf("Received heartbeat from server\n")

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
