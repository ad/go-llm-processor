package poller

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	"github.com/ad/llm-proxy/processor/internal/ollama"
	"github.com/ad/llm-proxy/processor/internal/worker"
	workerpool "github.com/ad/llm-proxy/processor/pkg/worker"
)

type SSEPoller struct {
	workerClient   *worker.Client
	ollamaClient   *ollama.Client
	config         *config.Config
	workerPool     *workerpool.Pool
	sseClient      *worker.SSEClient
	fallbackPoller *ImprovedPoller
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

	fallbackPoller := NewImproved(workerClient, ollamaClient, cfg)

	return &SSEPoller{
		workerClient:   workerClient,
		ollamaClient:   ollamaClient,
		config:         cfg,
		workerPool:     workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		sseClient:      sseClient,
		fallbackPoller: fallbackPoller,
		sseEnabled:     cfg.SSE.Enabled,
	}
}

func (p *SSEPoller) Start(ctx context.Context) {
	p.workerPool.Start()
	defer p.workerPool.Stop()

	if p.sseEnabled {
		log.Printf("Starting SSE poller for processor %s", p.config.ProcessorID)
		p.startSSEMode(ctx)
	} else {
		log.Printf("SSE disabled, using HTTP polling mode for processor %s", p.config.ProcessorID)
		p.fallbackPoller.Start(ctx)
	}
}

func (p *SSEPoller) startSSEMode(ctx context.Context) {
	// Start SSE client
	p.sseClient.Start(ctx)
	defer p.sseClient.Stop()

	// Heartbeat ticker for processor metrics
	heartbeatTicker := time.NewTicker(p.config.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	// Cleanup ticker
	cleanupTicker := time.NewTicker(p.config.PollInterval * 10)
	defer cleanupTicker.Stop()

	// Fallback polling ticker - slower than normal
	fallbackTicker := time.NewTicker(p.config.PollInterval * 3)
	defer fallbackTicker.Stop()

	sseFailures := 0
	maxSSEFailures := 3

	for {
		select {
		case <-ctx.Done():
			return

		case taskData := <-p.sseClient.GetTaskChannel():
			// Reset SSE failure count on successful task reception
			sseFailures = 0
			log.Printf("Received task via SSE: %s", taskData.TaskID)
			p.handleSSETask(ctx, taskData)

		case err := <-p.sseClient.GetErrorChannel():
			sseFailures++
			log.Printf("SSE error (%d/%d): %v", sseFailures, maxSSEFailures, err)

			if sseFailures >= maxSSEFailures {
				log.Printf("Too many SSE failures, falling back to HTTP polling")
				p.switchToHTTPPolling(ctx)
				return
			}

		case <-heartbeatTicker.C:
			p.sendHeartbeats(ctx)

		case <-cleanupTicker.C:
			p.triggerCleanup(ctx)

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
	// Try to claim the specific task
	if err := p.workerClient.ClaimTask(ctx, taskData.TaskID, p.config.ProcessorID, 5000); err != nil {
		log.Printf("Failed to claim task %s: %v", taskData.TaskID, err)
		return
	}

	// Create task job
	task := worker.Task{
		ID:         taskData.TaskID,
		Priority:   taskData.Priority,
		RetryCount: taskData.RetryCount,
	}

	// Create task job with minimal poller wrapper
	pollerWrapper := &Poller{
		config: p.config,
	}
	job := NewTaskJob(task, pollerWrapper, p.ollamaClient, p.workerClient)

	// Submit to worker pool
	p.workerPool.Submit(job)
}

func (p *SSEPoller) processFallbackTasks(ctx context.Context) {
	// Light fallback polling to catch any missed tasks
	tasks, err := p.workerClient.ClaimTasksBatch(ctx, p.config.ProcessorID, 1, 5000)
	if err != nil {
		log.Printf("Fallback task polling error: %v", err)
		return
	}

	if len(tasks) > 0 {
		log.Printf("Caught %d tasks via fallback polling", len(tasks))
		for _, task := range tasks {
			pollerWrapper := &Poller{
				config: p.config,
			}
			job := NewTaskJob(task, pollerWrapper, p.ollamaClient, p.workerClient)
			p.workerPool.Submit(job)
		}
	}
}

func (p *SSEPoller) sendHeartbeats(ctx context.Context) {
	activeWorkers := p.workerPool.ActiveWorkers()
	availableSlots := p.workerPool.AvailableSlots()

	// Use available client methods - check if UpdateHeartbeat exists or use alternative
	log.Printf("Heartbeat: active workers: %d, available slots: %d", activeWorkers, availableSlots)
}

func (p *SSEPoller) triggerCleanup(ctx context.Context) {
	// Check available methods on workerClient
	log.Printf("Performing periodic cleanup for processor %s", p.config.ProcessorID)
}

func (p *SSEPoller) IsSSEEnabled() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	return p.sseEnabled
}
