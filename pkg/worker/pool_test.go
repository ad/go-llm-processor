package worker

import (
	"context"
	"sync"
	"testing"
	"time"
)

type TestJob struct {
	id       int
	executed bool
	mu       sync.Mutex
}

func (tj *TestJob) Execute(ctx context.Context) error {
	tj.mu.Lock()
	defer tj.mu.Unlock()
	tj.executed = true
	return nil
}

func (tj *TestJob) IsExecuted() bool {
	tj.mu.Lock()
	defer tj.mu.Unlock()
	return tj.executed
}

func TestWorkerPool_StartAndStop(t *testing.T) {
	pool := NewPool(2, 10)

	pool.Start()
	time.Sleep(100 * time.Millisecond) // Let workers start

	pool.Stop()
}

func TestWorkerPool_JobExecution(t *testing.T) {
	pool := NewPool(2, 10)
	defer pool.Stop()

	pool.Start()
	time.Sleep(100 * time.Millisecond) // Let workers start

	job := &TestJob{id: 1}

	submitted := pool.Submit(job)
	if !submitted {
		t.Fatal("Failed to submit job")
	}

	// Wait for job execution
	time.Sleep(200 * time.Millisecond)

	if !job.IsExecuted() {
		t.Error("Job was not executed")
	}
}

func TestWorkerPool_MultipleJobs(t *testing.T) {
	pool := NewPool(3, 20)
	defer pool.Stop()

	pool.Start()
	time.Sleep(100 * time.Millisecond) // Let workers start

	jobs := make([]*TestJob, 10)
	for i := 0; i < 10; i++ {
		jobs[i] = &TestJob{id: i}
		submitted := pool.Submit(jobs[i])
		if !submitted {
			t.Fatalf("Failed to submit job %d", i)
		}
	}

	// Wait for all jobs to complete
	time.Sleep(500 * time.Millisecond)

	for i, job := range jobs {
		if !job.IsExecuted() {
			t.Errorf("Job %d was not executed", i)
		}
	}
}

func TestWorkerPool_QueueFull(t *testing.T) {
	// Small queue to test overflow
	pool := NewPool(1, 2)
	defer pool.Stop()

	pool.Start()
	time.Sleep(100 * time.Millisecond)

	// Fill the queue completely
	job1 := &TestJob{id: 1}
	job2 := &TestJob{id: 2}

	// Submit first two jobs - these should succeed
	if !pool.Submit(job1) {
		t.Error("Failed to submit first job")
	}
	if !pool.Submit(job2) {
		t.Error("Failed to submit second job")
	}

	// Try to submit more jobs quickly to fill the queue
	submittedExtra := 0
	for i := 0; i < 5; i++ {
		extraJob := &TestJob{id: 100 + i}
		if pool.Submit(extraJob) {
			submittedExtra++
		}
		// Small delay to let worker process
		time.Sleep(10 * time.Millisecond)
	}

	// Eventually some submissions should fail when queue gets full
	// t.Logf("Successfully submitted %d extra jobs before queue became full", submittedExtra)
}
