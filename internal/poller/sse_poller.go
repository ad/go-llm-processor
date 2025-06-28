package poller

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	sysmetrics "github.com/ad/llm-proxy/processor/internal/metrics"
	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/ollama"
	workerpool "github.com/ad/llm-proxy/processor/pkg/worker"
)

type SSEPoller struct {
	workerClient   *worker.Client
	ollamaClient   *ollama.Client
	config         *config.Config
	workerPool     *workerpool.Pool
	sseClient      *worker.SSEClient
	fallbackPoller *Poller
	activeTasks    map[string]string
	sseEnabled     bool
	mutex          sync.RWMutex
}

func NewSSEPoller(workerClient *worker.Client, ollamaClient *ollama.Client, cfg *config.Config) *SSEPoller {
	sseClient := worker.NewSSEClient(
		cfg.WorkerURL,
		cfg.InternalAPIKey,
		cfg.ProcessorID,
		worker.SSEConfig{
			Enabled:              cfg.SSE.Enabled,
			Endpoint:             cfg.SSE.Endpoint,
			ReconnectInterval:    cfg.SSE.ReconnectInterval,
			MaxReconnectAttempts: cfg.SSE.MaxReconnectAttempts,
			HeartbeatTimeout:     cfg.SSE.HeartbeatTimeout,
			HeartbeatInterval:    cfg.SSE.HeartbeatInterval,
			MaxDuration:          cfg.SSE.MaxDuration,
		},
	)

	fallbackPoller := New(workerClient, ollamaClient, cfg)

	return &SSEPoller{
		workerClient:   workerClient,
		ollamaClient:   ollamaClient,
		config:         cfg,
		workerPool:     workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		sseClient:      sseClient,
		activeTasks:    make(map[string]string),
		fallbackPoller: fallbackPoller,
		sseEnabled:     cfg.SSE.Enabled,
	}
}

func (p *SSEPoller) Start(ctx context.Context) {
	p.workerPool.Start()
	defer p.workerPool.Stop()

	if p.sseEnabled {
		log.Printf("Starting SSE poller for processor %s\n", p.config.ProcessorID)
		p.startSSEMode(ctx)
	} else {
		log.Printf("SSE disabled, using HTTP polling mode for processor %s\n", p.config.ProcessorID)
		p.fallbackPoller.Start(ctx)
	}
}

func (p *SSEPoller) startSSEMode(ctx context.Context) {
	// Start SSE client
	p.sseClient.Start(ctx)
	defer p.sseClient.Stop()

	// Heartbeat ticker for processor metrics
	heartbeatTicker := time.NewTicker(p.config.SSE.HeartbeatInterval)
	defer heartbeatTicker.Stop()
	p.sendHeartbeats(ctx)

	// // Cleanup ticker
	// cleanupTicker := time.NewTicker(p.config.PollInterval * 10)
	// defer cleanupTicker.Stop()

	// Fallback polling ticker - slower than normal
	fallbackTicker := time.NewTicker(p.config.PollInterval * 3)
	defer fallbackTicker.Stop()

	sseFailures := 0
	maxSSEFailures := p.config.SSE.MaxReconnectAttempts

	for {
		select {
		case <-ctx.Done():
			return

		case taskData := <-p.sseClient.GetTaskChannel():
			// Reset SSE failure count on successful task reception
			sseFailures = 0
			// log.Printf("Received task via SSE: %s\n", taskData.TaskID)
			p.handleSSETask(ctx, taskData)

		case err := <-p.sseClient.GetErrorChannel():
			sseFailures++
			log.Printf("SSE error (%d/%d): %v\n", sseFailures, maxSSEFailures, err)

			if sseFailures >= maxSSEFailures {
				log.Printf("Too many SSE failures, falling back to HTTP polling\n")
				p.switchToHTTPPolling(ctx)
				return
			}

		case <-heartbeatTicker.C:
			p.sendHeartbeats(ctx)
			// Сброс ошибок при успешном heartbeat (если heartbeat не вызывает ошибку)
			sseFailures = 0

		// case <-cleanupTicker.C:
		// 	p.triggerCleanup(ctx)

		case <-fallbackTicker.C:
			// Fallback polling to catch any missed tasks
			p.processFallbackTasks(ctx)
		}
	}
}

func (p *SSEPoller) switchToHTTPPolling(ctx context.Context) {
	log.Println("Switching to HTTP polling mode")
	p.mutex.Lock()
	p.sseEnabled = false
	p.mutex.Unlock()

	p.sseClient.Stop()
	p.fallbackPoller.Start(ctx)
}

func (p *SSEPoller) handleSSETask(ctx context.Context, taskData worker.TaskAvailableData) {
	// log.Printf("handleSSETask: trying to claim task %s", taskData.TaskID)
	if err := p.workerClient.ClaimTask(ctx, taskData.TaskID, p.config.ProcessorID, 5000); err != nil {
		log.Printf("Failed to claim task %s: %v\n", taskData.TaskID, err)
		return
	}
	log.Printf("handleSSETask: successfully claimed task %s", taskData.TaskID)

	// Create task job
	task := worker.Task{
		ID:           taskData.TaskID,
		Priority:     taskData.Priority,
		RetryCount:   taskData.RetryCount,
		ProductData:  taskData.ProductData,
		OllamaParams: taskData.OllamaParams,
	}

	job := NewTaskJob(task, p.fallbackPoller, p.ollamaClient, p.workerClient)
	job.OnDone = func(result string, err error) {
		// if err := p.workerClient.ReleaseTask(ctx, task.ID); err != nil {
		// 	log.Printf("Failed to release task %s: %v", task.ID, err)
		// }
		p.removeActiveTask(task.ID)
	}

	ok := p.workerPool.Submit(job)
	if ok {
		p.addActiveTask(taskData.TaskID)
		// log.Printf("handleSSETask: submitted task %s to worker pool", taskData.TaskID)
	} else {
		log.Printf("handleSSETask: failed to submit task %s to worker pool (queue full)", taskData.TaskID)
	}
}

func (p *SSEPoller) processFallbackTasks(ctx context.Context) {
	// Light fallback polling to catch any missed tasks
	tasks, err := p.workerClient.ClaimTasksBatch(ctx, p.config.ProcessorID, 1, 5000)
	if err != nil {
		log.Printf("Fallback task polling error: %v\n", err)
		return
	}

	if len(tasks) > 0 {
		log.Printf("Caught %d tasks via fallback polling\n", len(tasks))
		for _, task := range tasks {
			job := NewTaskJob(task, p.fallbackPoller, p.ollamaClient, p.workerClient)
			p.workerPool.Submit(job)
		}
	}
}

func (p *SSEPoller) addActiveTask(taskID string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.activeTasks[taskID] = p.config.ProcessorID
}

func (p *SSEPoller) removeActiveTask(taskID string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	delete(p.activeTasks, taskID)
}

func (p *SSEPoller) sendHeartbeats(ctx context.Context) {
	p.mutex.RLock()
	activeTasks := make([]string, 0, len(p.activeTasks))
	for taskID := range p.activeTasks {
		activeTasks = append(activeTasks, taskID)
	}
	currentQueueSize := len(p.activeTasks)
	p.mutex.RUnlock()

	// Send task heartbeats
	for _, taskID := range activeTasks {
		if err := p.workerClient.SendHeartbeat(ctx, taskID, p.config.ProcessorID); err != nil {
			log.Printf("Error sending heartbeat for task %s: %v\n", taskID, err)
			p.removeActiveTask(taskID)
		}
	}

	// Send processor metrics heartbeat
	systemMetrics := sysmetrics.GetSystemMetrics(currentQueueSize)
	if err := p.workerClient.SendProcessorHeartbeat(
		ctx,
		p.config.ProcessorID,
		&systemMetrics.CPUUsage,
		&systemMetrics.MemoryUsage,
		&systemMetrics.QueueSize,
	); err != nil {
		log.Printf("Error sending processor heartbeat: %v\n", err)
	}
}

// func (p *SSEPoller) triggerCleanup(ctx context.Context) {
// 	// Check available methods on workerClient
// 	log.Printf("Performing periodic cleanup for processor %s", p.config.ProcessorID)
// }

func (p *SSEPoller) IsSSEEnabled() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return p.sseEnabled
}
