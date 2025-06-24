package metrics

import (
	"sync"
	"testing"
	"time"
)

func TestMetrics_IncrementCounters(t *testing.T) {
	m := &Metrics{}

	m.IncrementProcessed()
	m.IncrementCompleted(100 * time.Millisecond)
	m.IncrementFailed()
	m.IncrementRetried()

	snapshot := m.GetSnapshot()

	if snapshot.TasksProcessed != 1 {
		t.Errorf("Expected TasksProcessed=1, got %d", snapshot.TasksProcessed)
	}
	if snapshot.TasksCompleted != 1 {
		t.Errorf("Expected TasksCompleted=1, got %d", snapshot.TasksCompleted)
	}
	if snapshot.TasksFailed != 1 {
		t.Errorf("Expected TasksFailed=1, got %d", snapshot.TasksFailed)
	}
	if snapshot.TasksRetried != 1 {
		t.Errorf("Expected TasksRetried=1, got %d", snapshot.TasksRetried)
	}
}

func TestMetrics_AverageProcessTime(t *testing.T) {
	m := &Metrics{}

	// Add multiple completion times
	m.IncrementCompleted(100 * time.Millisecond)
	m.IncrementCompleted(200 * time.Millisecond)
	m.IncrementCompleted(300 * time.Millisecond)

	snapshot := m.GetSnapshot()

	expectedAvg := 200 * time.Millisecond
	if snapshot.AvgProcessTime != expectedAvg {
		t.Errorf("Expected average %v, got %v", expectedAvg, snapshot.AvgProcessTime)
	}

	// Test with single completion
	m2 := &Metrics{}
	m2.IncrementCompleted(150 * time.Millisecond)
	snapshot2 := m2.GetSnapshot()

	if snapshot2.AvgProcessTime != 150*time.Millisecond {
		t.Errorf("Expected single completion time %v, got %v", 150*time.Millisecond, snapshot2.AvgProcessTime)
	}
}

func TestMetrics_Reset(t *testing.T) {
	m := &Metrics{}

	m.IncrementProcessed()
	m.IncrementCompleted(100 * time.Millisecond)
	m.IncrementFailed()

	m.Reset()
	snapshot := m.GetSnapshot()

	if snapshot.TasksProcessed != 0 {
		t.Errorf("Expected TasksProcessed=0 after reset, got %d", snapshot.TasksProcessed)
	}
	if snapshot.TasksCompleted != 0 {
		t.Errorf("Expected TasksCompleted=0 after reset, got %d", snapshot.TasksCompleted)
	}
	if snapshot.TasksFailed != 0 {
		t.Errorf("Expected TasksFailed=0 after reset, got %d", snapshot.TasksFailed)
	}
	if snapshot.AvgProcessTime != 0 {
		t.Errorf("Expected AvgProcessTime=0 after reset, got %v", snapshot.AvgProcessTime)
	}
}

func TestMetrics_Concurrency(t *testing.T) {
	m := &Metrics{}

	// Use sync.WaitGroup for proper synchronization
	var wg sync.WaitGroup
	const numGoroutines = 10
	const opsPerGoroutine = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				m.IncrementProcessed()
				m.IncrementCompleted(time.Millisecond)
			}
		}()
	}

	// Wait for all goroutines to complete
	wg.Wait()

	snapshot := m.GetSnapshot()

	expectedProcessed := int64(numGoroutines * opsPerGoroutine)
	if snapshot.TasksProcessed != expectedProcessed {
		t.Errorf("Expected TasksProcessed=%d, got %d", expectedProcessed, snapshot.TasksProcessed)
	}
	if snapshot.TasksCompleted != expectedProcessed {
		t.Errorf("Expected TasksCompleted=%d, got %d", expectedProcessed, snapshot.TasksCompleted)
	}

	// Check that average process time is calculated correctly
	if snapshot.AvgProcessTime != time.Millisecond {
		t.Errorf("Expected AvgProcessTime=%v, got %v", time.Millisecond, snapshot.AvgProcessTime)
	}
}

func TestMetrics_ZeroValues(t *testing.T) {
	m := &Metrics{}
	snapshot := m.GetSnapshot()

	if snapshot.TasksProcessed != 0 {
		t.Errorf("Expected TasksProcessed=0 for new metrics, got %d", snapshot.TasksProcessed)
	}
	if snapshot.AvgProcessTime != 0 {
		t.Errorf("Expected AvgProcessTime=0 for new metrics, got %v", snapshot.AvgProcessTime)
	}
}

func TestMetrics_SingleIncrement(t *testing.T) {
	m := &Metrics{}

	// Test individual increments
	m.IncrementProcessed()
	snapshot := m.GetSnapshot()
	if snapshot.TasksProcessed != 1 {
		t.Errorf("Expected TasksProcessed=1, got %d", snapshot.TasksProcessed)
	}

	m.IncrementFailed()
	snapshot = m.GetSnapshot()
	if snapshot.TasksFailed != 1 {
		t.Errorf("Expected TasksFailed=1, got %d", snapshot.TasksFailed)
	}

	m.IncrementRetried()
	snapshot = m.GetSnapshot()
	if snapshot.TasksRetried != 1 {
		t.Errorf("Expected TasksRetried=1, got %d", snapshot.TasksRetried)
	}
}
