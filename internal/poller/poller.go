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
	"github.com/ad/llm-proxy/processor/internal/promptutils"
	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/metrics"
	"github.com/ad/llm-proxy/processor/pkg/ollama"
	"github.com/ad/llm-proxy/processor/pkg/retry"
	workerpool "github.com/ad/llm-proxy/processor/pkg/worker"
)

type HeartbeatMode int

const (
	HeartbeatTasksOnly HeartbeatMode = iota
	HeartbeatWithMetrics
)

type Poller struct {
	workerClient      WorkerClient
	ollamaClient      OllamaClient
	config            *config.Config
	workerPool        *workerpool.Pool
	activeTasks       map[string]string
	tasksMutex        sync.RWMutex
	backoffMultiplier float64
	maxBackoff        time.Duration
	heartbeatMode     HeartbeatMode
	taskSource        TaskSource // внешний источник задач (push)
}

func New(workerClient *worker.Client, ollamaClient *ollama.Client, cfg *config.Config, heartbeatMode ...HeartbeatMode) *Poller {
	mode := HeartbeatWithMetrics
	if len(heartbeatMode) > 0 {
		mode = heartbeatMode[0]
	}
	return &Poller{
		workerClient:      workerClient,
		ollamaClient:      ollamaClient,
		config:            cfg,
		workerPool:        workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:       make(map[string]string),
		backoffMultiplier: 1.0,
		maxBackoff:        time.Minute * 5,
		heartbeatMode:     mode,
		taskSource:        nil,
	}
}

func (p *Poller) SetTaskSource(ts TaskSource) {
	p.taskSource = ts
}

func (p *Poller) Start(ctx context.Context) {
	p.workerPool.Start()
	defer p.workerPool.Stop()

	initialDelay := p.config.InitialDelay
	if initialDelay != 0 {
		time.Sleep(initialDelay)
	}

	if p.taskSource != nil {
		// Push-модель: запускаем TaskSource
		p.taskSource.Start(ctx, func(ctx context.Context, task worker.Task) {
			p.addActiveTask(task.ID)
			job := NewTaskJob(task, p, p.ollamaClient, p.workerClient)
			p.workerPool.Submit(job)
		})
		return
	}

	// Polling-модель
	p.adaptivePollingLoop(ctx)
}

func (p *Poller) adaptivePollingLoop(ctx context.Context) {
	// log.Printf("adaptivePollingLoop started")
	heartbeatTicker := time.NewTicker(p.config.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	cleanupTicker := time.NewTicker(p.config.PollInterval * 10)
	defer cleanupTicker.Stop()

	var workStealingTicker *time.Ticker
	if p.config.WorkStealing.Enabled {
		workStealingTicker = time.NewTicker(p.config.WorkStealing.Interval)
		defer workStealingTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			p.sendHeartbeats(ctx)
		case <-cleanupTicker.C:
			p.triggerCleanup(ctx)
		case <-func() <-chan time.Time {
			if workStealingTicker != nil {
				return workStealingTicker.C
			}
			return make(chan time.Time) // never triggers
		}():
			p.tryWorkStealing(ctx)
		default:
			// Adaptive polling interval
			interval := p.calculatePollInterval()
			const step = 20 * time.Millisecond
			elapsed := time.Duration(0)
			for elapsed < interval {
				select {
				case <-ctx.Done():
					return
				case <-time.After(step):
					elapsed += step
				}
			}
			p.processTasks(ctx)
		}
	}
}

func (p *Poller) calculatePollInterval() time.Duration {
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

	// Для тестов можно отключить jitter
	jitter := time.Duration(0)
	if p.config != nil && !p.config.DisableJitter {
		jitter = time.Duration(rand.Intn(5)) * time.Second
	}
	return interval + jitter
}

func (p *Poller) processTasks(ctx context.Context) {
	availableSlots := p.workerPool.AvailableSlots()
	if availableSlots == 0 {
		return
	}

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

	slotsLeft := availableSlots
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
			if slotsLeft > 0 {
				p.addActiveTask(task.ID)
				job := NewTaskJob(task, p, p.ollamaClient, p.workerClient)
				if p.workerPool.Submit(job) {
					slotsLeft--
				} else {
					log.Printf("Worker pool full, releasing task %s\n", task.ID)
					p.removeActiveTask(task.ID)
					p.workerClient.ReleaseTask(ctx, task.ID)
				}
			} else {
				log.Printf("No slots left, releasing task %s\n", task.ID)
				p.workerClient.ReleaseTask(ctx, task.ID)
			}
		}
	}
}

// tryWorkStealing attempts to steal tasks from overloaded processors
func (p *Poller) tryWorkStealing(ctx context.Context) {
	// Проверяем, есть ли у нас свободная емкость для воровства задач
	availableSlots := p.workerPool.AvailableSlots()
	capacity := float64(availableSlots) / float64(p.config.WorkerCount)

	// Если у нас недостаточно свободного места, не воруем задачи
	if capacity < p.config.WorkStealing.MinCapacity {
		return
	}

	// Определяем количество задач для воровства
	maxStealCount := p.config.WorkStealing.MaxStealCount
	if availableSlots < maxStealCount {
		maxStealCount = availableSlots
	}

	if maxStealCount == 0 {
		return
	}

	timeoutMs := int(p.config.RequestTimeout.Milliseconds())

	// Пытаемся украсть задачи
	stolenTasks, err := p.workerClient.WorkSteal(ctx, p.config.ProcessorID, maxStealCount, timeoutMs)
	if err != nil {
		log.Printf("Error during work stealing: %v", err)
		return
	}

	if len(stolenTasks) == 0 {
		return
	}

	log.Printf("Successfully stole %d tasks from overloaded processors", len(stolenTasks))

	// Обрабатываем украденные задачи
	slotsLeft := availableSlots
	for _, task := range stolenTasks {
		select {
		case <-ctx.Done():
			// Контекст отменен, возвращаем оставшиеся задачи
			for i := len(stolenTasks) - slotsLeft + len(stolenTasks) - 1; i < len(stolenTasks); i++ {
				if i >= 0 {
					p.workerClient.ReleaseTask(ctx, stolenTasks[i].ID)
				}
			}
			return
		default:
			if slotsLeft > 0 {
				p.addActiveTask(task.ID)
				job := NewTaskJob(task, p, p.ollamaClient, p.workerClient)
				if p.workerPool.Submit(job) {
					slotsLeft--
				} else {
					log.Printf("Worker pool full, releasing stolen task %s", task.ID)
					p.removeActiveTask(task.ID)
					p.workerClient.ReleaseTask(ctx, task.ID)
				}
			} else {
				log.Printf("No slots left, releasing stolen task %s", task.ID)
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

func (p *Poller) addActiveTask(taskID string) {
	p.tasksMutex.Lock()
	defer p.tasksMutex.Unlock()
	p.activeTasks[taskID] = p.config.ProcessorID
}

func (p *Poller) removeActiveTask(taskID string) {
	p.tasksMutex.Lock()
	defer p.tasksMutex.Unlock()
	delete(p.activeTasks, taskID)
}

func (p *Poller) sendHeartbeats(ctx context.Context) {
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

	if p.heartbeatMode == HeartbeatWithMetrics {
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
}

func (p *Poller) triggerCleanup(ctx context.Context) {
	if err := p.workerClient.TriggerCleanup(ctx); err != nil {
		log.Printf("Error triggering cleanup: %v\n", err)
	}
}

type TaskJob struct {
	task         worker.Task
	poller       *Poller
	ollamaClient OllamaClient
	workerClient WorkerClient
	OnDone       func(result string, err error)
}

func NewTaskJob(task worker.Task, poller *Poller, ollamaClient OllamaClient, workerClient WorkerClient) *TaskJob {
	return &TaskJob{
		task:         task,
		poller:       poller,
		ollamaClient: ollamaClient,
		workerClient: workerClient,
	}
}

func (tj *TaskJob) Execute(ctx context.Context) error {
	startTime := time.Now()
	metrics.GlobalMetrics.IncrementProcessed()

	ollamaParams, err := tj.task.ParseOllamaParams()
	if err != nil {
		log.Printf("Error parsing ollama params for task %s: %v, using defaults\n", tj.task.ID, err)
		ollamaParams = nil
	}

	taskCtx, cancel := context.WithTimeout(ctx, tj.poller.config.RequestTimeout)
	defer cancel()

	var result string
	retryConfig := retry.DefaultConfig()
	retryConfig.MaxRetries = tj.poller.config.MaxRetries

	err = retry.Do(taskCtx, retryConfig, func() error {
		modelToUse := tj.poller.config.ModelName

		if ollamaParams != nil && ollamaParams.Model != "" {
			modelToUse = ollamaParams.Model
		}

		if ollamaParams == nil {
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
			}
		}

		if ollamaParams.Prompt == "" {
			ollamaParams.Prompt = promptutils.GetDefaultPrompt()
		}

		if err := tj.ollamaClient.EnsureModelAvailable(modelToUse); err != nil {
			// log.Printf("Модель %s недоступна и не может быть скачана: %v\n", modelToUse, err)
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
		// log.Printf("Error generating description for task %s after retries: %v\n", tj.task.ID, err)
		metrics.GlobalMetrics.IncrementFailed()
		tj.poller.removeActiveTask(tj.task.ID)
		requeueErr := tj.workerClient.RequeueTask(ctx, tj.task.ID, tj.poller.config.ProcessorID, "generation/model unavailable")
		if tj.OnDone != nil {
			tj.OnDone("", err)
		}
		if requeueErr != nil {
			log.Printf("Ошибка при RequeueTask: %v\n", requeueErr)
			return requeueErr
		}
		return err
	}

	if err := tj.workerClient.CompleteTask(ctx, tj.task.ID, tj.poller.config.ProcessorID, "completed", result, ""); err != nil {
		metrics.GlobalMetrics.IncrementFailed()
		tj.poller.removeActiveTask(tj.task.ID)
		if tj.OnDone != nil {
			tj.OnDone("", err)
		}
		return fmt.Errorf("complete task %s: %w", tj.task.ID, err)
	}

	processingTime := time.Since(startTime)
	metrics.GlobalMetrics.IncrementCompleted(processingTime)
	tj.poller.removeActiveTask(tj.task.ID)
	if tj.OnDone != nil {
		tj.OnDone(result, nil)
	}
	log.Printf("Task %s completed successfully in %v\n", tj.task.ID, processingTime)
	return nil
}
