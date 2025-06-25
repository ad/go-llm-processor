package worker

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
)

type Job interface {
	Execute(ctx context.Context) error
}

type Pool struct {
	workerCount   int
	jobQueue      chan Job
	wg            sync.WaitGroup
	ctx           context.Context
	cancel        context.CancelFunc
	activeWorkers int32
}

func NewPool(workerCount, queueSize int) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{
		workerCount: workerCount,
		jobQueue:    make(chan Job, queueSize),
		ctx:         ctx,
		cancel:      cancel,
	}
}

func (p *Pool) Start() {
	log.Printf("Starting worker pool with %d workers\n", p.workerCount)

	for i := 0; i < p.workerCount; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
}

func (p *Pool) worker(id int) {
	defer p.wg.Done()
	log.Printf("Worker %d started\n", id)

	for {
		select {
		case <-p.ctx.Done():
			log.Printf("Worker %d stopping\n", id)
			return
		case job := <-p.jobQueue:
			if job != nil {
				atomic.AddInt32(&p.activeWorkers, 1)
				if err := job.Execute(p.ctx); err != nil {
					log.Printf("Worker %d: job execution failed: %v\n", id, err)
				}
				atomic.AddInt32(&p.activeWorkers, -1)
			}
		}
	}
}

func (p *Pool) Submit(job Job) bool {
	select {
	case p.jobQueue <- job:
		return true
	default:
		return false
	}
}

func (p *Pool) Stop() {
	log.Println("Stopping worker pool...")
	close(p.jobQueue)
	p.cancel()
	p.wg.Wait()
	log.Println("Worker pool stopped")
}

func (p *Pool) ActiveWorkers() int {
	return int(atomic.LoadInt32(&p.activeWorkers))
}

func (p *Pool) AvailableSlots() int {
	queueLen := len(p.jobQueue)
	queueCap := cap(p.jobQueue)
	return queueCap - queueLen
}
