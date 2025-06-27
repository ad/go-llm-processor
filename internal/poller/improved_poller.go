package poller

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	sysmetrics "github.com/ad/llm-proxy/processor/internal/metrics"
	"github.com/ad/llm-proxy/processor/internal/ollama"
	"github.com/ad/llm-proxy/processor/internal/promptutils"
	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/metrics"
	"github.com/ad/llm-proxy/processor/pkg/retry"
	workerpool "github.com/ad/llm-proxy/processor/pkg/worker"
)

type ImprovedPoller struct {
	workerClient      *worker.Client
	ollamaClient      *ollama.Client
	config            *config.Config
	workerPool        *workerpool.Pool
	activeTasks       map[string]string
	tasksMutex        sync.RWMutex
	backoffMultiplier float64
	maxBackoff        time.Duration
}

func NewImproved(workerClient *worker.Client, ollamaClient *ollama.Client, cfg *config.Config) *ImprovedPoller {
	return &ImprovedPoller{
		workerClient:      workerClient,
		ollamaClient:      ollamaClient,
		config:            cfg,
		workerPool:        workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:       make(map[string]string),
		backoffMultiplier: 1.0,
		maxBackoff:        time.Minute * 5,
	}
}

func (p *ImprovedPoller) Start(ctx context.Context) {
	p.workerPool.Start()
	defer p.workerPool.Stop()

	// Staggered startup to avoid thundering herd
	initialDelay := time.Duration(rand.Intn(30)) * time.Second
	time.Sleep(initialDelay)

	// Adaptive polling with exponential backoff
	p.adaptivePollingLoop(ctx)
}

func (p *ImprovedPoller) adaptivePollingLoop(ctx context.Context) {
	heartbeatTicker := time.NewTicker(p.config.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	cleanupTicker := time.NewTicker(p.config.PollInterval * 10)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			p.sendHeartbeats(ctx)
		case <-cleanupTicker.C:
			p.triggerCleanup(ctx)
		default:
			// Adaptive polling interval
			interval := p.calculatePollInterval()
			time.Sleep(interval)
			p.processTasks(ctx)
		}
	}
}

func (p *ImprovedPoller) calculatePollInterval() time.Duration {
	baseInterval := p.config.PollInterval

	// Если воркер пул загружен, увеличиваем интервал
	poolUtilization := float64(p.workerPool.ActiveWorkers()) / float64(p.config.WorkerCount)

	if poolUtilization > 0.8 {
		p.backoffMultiplier = minFloat(p.backoffMultiplier*1.5, 10.0)
	} else if poolUtilization < 0.2 {
		p.backoffMultiplier = maxFloat(p.backoffMultiplier*0.8, 1.0)
	}

	interval := time.Duration(float64(baseInterval) * p.backoffMultiplier)
	if interval > p.maxBackoff {
		interval = p.maxBackoff
	}

	// Добавляем jitter для избежания синхронизации
	jitter := time.Duration(rand.Intn(5)) * time.Second
	return interval + jitter
}

func (p *ImprovedPoller) processTasks(ctx context.Context) {
	// Batch claim multiple tasks at once
	availableSlots := p.workerPool.AvailableSlots()
	if availableSlots == 0 {
		return
	}

	// Request optimal number of tasks
	batchSize := min(availableSlots, p.config.MaxBatchSize)

	tasks, err := p.workerClient.ClaimTasksBatch(ctx, p.config.ProcessorID, batchSize, int(p.config.RequestTimeout.Milliseconds()))
	if err != nil {
		log.Printf("Error claiming tasks batch: %v\n", err)
		return
	}

	if len(tasks) == 0 {
		return
	}

	log.Printf("Claimed %d tasks in batch\n", len(tasks))

	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
			p.addActiveTask(task.ID)
			job := NewImprovedTaskJob(task, p, p.ollamaClient, p.workerClient)

			if !p.workerPool.Submit(job) {
				log.Printf("Worker pool full, releasing task %s\n", task.ID)
				p.removeActiveTask(task.ID)
				// Release task back to queue
				p.workerClient.ReleaseTask(ctx, task.ID)
			}
		}
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Остальные методы остаются такими же...
func (p *ImprovedPoller) addActiveTask(taskID string) {
	p.tasksMutex.Lock()
	defer p.tasksMutex.Unlock()
	p.activeTasks[taskID] = p.config.ProcessorID
}

func (p *ImprovedPoller) removeActiveTask(taskID string) {
	p.tasksMutex.Lock()
	defer p.tasksMutex.Unlock()
	delete(p.activeTasks, taskID)
}

func (p *ImprovedPoller) sendHeartbeats(ctx context.Context) {
	p.tasksMutex.RLock()
	activeTasks := make([]string, 0, len(p.activeTasks))
	for taskID := range p.activeTasks {
		activeTasks = append(activeTasks, taskID)
	}
	currentQueueSize := len(p.activeTasks)
	p.tasksMutex.RUnlock()

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

func (p *ImprovedPoller) triggerCleanup(ctx context.Context) {
	if err := p.workerClient.TriggerCleanup(ctx); err != nil {
		log.Printf("Error triggering cleanup: %v\n", err)
	}
}

type ImprovedTaskJob struct {
	task         worker.Task
	poller       *ImprovedPoller
	ollamaClient *ollama.Client
	workerClient *worker.Client
}

func NewImprovedTaskJob(task worker.Task, poller *ImprovedPoller, ollamaClient *ollama.Client, workerClient *worker.Client) *ImprovedTaskJob {
	return &ImprovedTaskJob{
		task:         task,
		poller:       poller,
		ollamaClient: ollamaClient,
		workerClient: workerClient,
	}
}

func (tj *ImprovedTaskJob) Execute(ctx context.Context) error {
	startTime := time.Now()
	log.Printf("Processing task %s in improved worker pool", tj.task.ID)

	metrics.GlobalMetrics.IncrementProcessed()

	// Parse ollama parameters from task
	ollamaParams, err := tj.task.ParseOllamaParams()
	if err != nil {
		log.Printf("Error parsing ollama params for task %s: %v, using defaults\n", tj.task.ID, err)
		ollamaParams = nil
	}

	// Create timeout context for Ollama generation
	taskCtx, cancel := context.WithTimeout(ctx, tj.poller.config.RequestTimeout)
	defer cancel()

	var result string

	// Use retry mechanism for Ollama generation
	retryConfig := retry.DefaultConfig()
	retryConfig.MaxRetries = tj.poller.config.MaxRetries

	err = retry.Do(taskCtx, retryConfig, func() error {
		modelToUse := tj.poller.config.ModelName

		// Корректно формируем prompt и userPrompt для chat/completions
		if ollamaParams != nil {
			if ollamaParams.Model != "" {
				modelToUse = ollamaParams.Model
			}
		} else {
			temp := 0.3
			topP := 0.9
			topK := 40
			repeatPenalty := 1.1

			ollamaParams = &ollama.OllamaParams{
				Model:         modelToUse,
				Temperature:   &temp,
				TopP:          &topP,
				TopK:          &topK,
				RepeatPenalty: &repeatPenalty,
				Prompt:        promptutils.GetDefaultPrompt(),
			}
		}

		if err := tj.ollamaClient.EnsureModelAvailable(modelToUse); err != nil {
			log.Printf("Модель %s недоступна и не может быть скачана: %v\n", modelToUse, err)
			return err
		}

		var genErr error
		result, genErr = tj.ollamaClient.GenerateWithParams(taskCtx, modelToUse, tj.task.ProductData, ollamaParams)

		if genErr != nil {
			metrics.GlobalMetrics.IncrementRetried()
		}
		return genErr
	})

	if err != nil {
		log.Printf("Error generating description for task %s after retries: %v\n", tj.task.ID, err)
		metrics.GlobalMetrics.IncrementFailed()
		tj.poller.removeActiveTask(tj.task.ID)
		return tj.workerClient.CompleteTask(ctx, tj.task.ID, tj.poller.config.ProcessorID, "failed", "", fmt.Sprintf("Generation failed after retries: %v", err))
	}

	// Complete task with result
	if err := tj.workerClient.CompleteTask(ctx, tj.task.ID, tj.poller.config.ProcessorID, "completed", result, ""); err != nil {
		metrics.GlobalMetrics.IncrementFailed()
		tj.poller.removeActiveTask(tj.task.ID)
		return fmt.Errorf("complete task %s: %w", tj.task.ID, err)
	}

	processingTime := time.Since(startTime)
	metrics.GlobalMetrics.IncrementCompleted(processingTime)
	tj.poller.removeActiveTask(tj.task.ID)
	log.Printf("Task %s completed successfully in %v\n", tj.task.ID, processingTime)
	return nil
}
