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

type Poller struct {
	workerClient *worker.Client
	ollamaClient *ollama.Client
	config       *config.Config
	workerPool   *workerpool.Pool
	activeTasks  map[string]string // taskID -> processorID
	tasksMutex   sync.RWMutex
}

func New(workerClient *worker.Client, ollamaClient *ollama.Client, cfg *config.Config) *Poller {
	return &Poller{
		workerClient: workerClient,
		ollamaClient: ollamaClient,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:  make(map[string]string),
	}
}

func (p *Poller) Start(ctx context.Context) {
	// Start worker pool
	p.workerPool.Start()
	defer p.workerPool.Stop()

	// Start cleanup routine
	cleanupTicker := time.NewTicker(p.config.PollInterval * 10) // Cleanup every 10 polling cycles
	defer cleanupTicker.Stop()

	// Start heartbeat for active tasks
	heartbeatTicker := time.NewTicker(p.config.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()

	log.Printf("Starting task poller with processor ID: %s, interval: %v\n", p.config.ProcessorID, p.config.PollInterval)

	for {
		select {
		case <-ctx.Done():
			log.Println("Task poller stopping...")
			return
		case <-ticker.C:
			p.processTasks(ctx)
		case <-cleanupTicker.C:
			p.triggerCleanup(ctx)
		case <-heartbeatTicker.C:
			p.sendHeartbeats(ctx)
		}
	}
}

func (p *Poller) processTasks(ctx context.Context) {
	tasks, err := p.workerClient.GetPendingTasks(ctx)
	if err != nil {
		log.Printf("Error getting pending tasks: %v\n", err)
		return
	}

	if len(tasks) == 0 {
		return
	}

	log.Printf("Processing %d pending tasks\n", len(tasks))

	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
			// Try to claim the task atomically
			if err := p.workerClient.ClaimTask(ctx, task.ID, p.config.ProcessorID, int(p.config.RequestTimeout.Milliseconds())); err != nil {
				log.Printf("Failed to claim task %s: %v\n", task.ID, err)
				continue
			}

			// Track active task
			p.addActiveTask(task.ID)

			// Submit task to worker pool
			job := NewTaskJob(task, p, p.ollamaClient, p.workerClient)
			if !p.workerPool.Submit(job) {
				log.Printf("Worker pool queue is full, releasing task %s\n", task.ID)
				p.removeActiveTask(task.ID)
				// TODO: Release the claimed task back to pending
			}
		}
	}
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

func (p *Poller) getActiveTasks() []string {
	p.tasksMutex.RLock()
	defer p.tasksMutex.RUnlock()

	tasks := make([]string, 0, len(p.activeTasks))
	for taskID := range p.activeTasks {
		tasks = append(tasks, taskID)
	}
	return tasks
}

func (p *Poller) triggerCleanup(ctx context.Context) {
	if err := p.workerClient.TriggerCleanup(ctx); err != nil {
		log.Printf("Error triggering cleanup: %v\n", err)
	}
}

func (p *Poller) sendHeartbeats(ctx context.Context) {
	activeTasks := p.getActiveTasks()

	for _, taskID := range activeTasks {
		if err := p.workerClient.SendHeartbeat(ctx, taskID, p.config.ProcessorID); err != nil {
			log.Printf("Error sending heartbeat for task %s: %v\n", taskID, err)
			// Remove from active tasks if heartbeat fails
			p.removeActiveTask(taskID)
		}
	}
}
