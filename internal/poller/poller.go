package poller

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/ollama"
	workerpool "github.com/ad/llm-proxy/processor/pkg/worker"
)

type Poller struct {
	workerClient *worker.Client
	ollamaClient *ollama.Client
	config       *config.Config
	workerPool   *workerpool.Pool
	activeTasks  map[string]string // taskID -> processorID
	tasksMutex   sync.RWMutex
	taskSource   TaskSource
}

func New(workerClient *worker.Client, ollamaClient *ollama.Client, cfg *config.Config, taskSource TaskSource) *Poller {
	return &Poller{
		workerClient: workerClient,
		ollamaClient: ollamaClient,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:  make(map[string]string),
		taskSource:   taskSource,
	}
}

func (p *Poller) Start(ctx context.Context) {
	p.workerPool.Start()
	defer p.workerPool.Stop()

	cleanupTicker := time.NewTicker(p.config.PollInterval * 10)
	defer cleanupTicker.Stop()

	heartbeatTicker := time.NewTicker(p.config.HeartbeatInterval)
	defer heartbeatTicker.Stop()

	go p.taskSource.Start(ctx, func(ctx context.Context, task worker.Task) {
		p.handleTask(ctx, task)
	})

	for {
		select {
		case <-ctx.Done():
			log.Println("Task poller stopping...")
			return
		case <-cleanupTicker.C:
			p.triggerCleanup(ctx)
		case <-heartbeatTicker.C:
			p.sendHeartbeats(ctx)
		}
	}
}

func (p *Poller) handleTask(ctx context.Context, task worker.Task) {
	p.addActiveTask(task.ID)
	job := NewTaskJob(task, p, p.ollamaClient, p.workerClient)
	if !p.workerPool.Submit(job) {
		log.Printf("Worker pool queue is full, releasing task %s\n", task.ID)
		p.removeActiveTask(task.ID)
		// TODO: Release the claimed task back to pending
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
