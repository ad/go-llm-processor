package metrics

import (
	"sync"
	"time"
)

type Metrics struct {
	mu               sync.RWMutex
	TasksProcessed   int64
	TasksCompleted   int64
	TasksFailed      int64
	TasksRetried     int64
	TotalProcessTime time.Duration
	AvgProcessTime   time.Duration
	LastUpdate       time.Time
}

var GlobalMetrics = &Metrics{
	LastUpdate: time.Now(),
}

func (m *Metrics) IncrementProcessed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TasksProcessed++
	m.LastUpdate = time.Now()
}

func (m *Metrics) IncrementCompleted(processingTime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TasksCompleted++
	m.TotalProcessTime += processingTime

	if m.TasksCompleted > 0 {
		m.AvgProcessTime = m.TotalProcessTime / time.Duration(m.TasksCompleted)
	}

	m.LastUpdate = time.Now()
}

func (m *Metrics) IncrementFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TasksFailed++
	m.LastUpdate = time.Now()
}

func (m *Metrics) IncrementRetried() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TasksRetried++
	m.LastUpdate = time.Now()
}

func (m *Metrics) GetSnapshot() Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Metrics{
		TasksProcessed:   m.TasksProcessed,
		TasksCompleted:   m.TasksCompleted,
		TasksFailed:      m.TasksFailed,
		TasksRetried:     m.TasksRetried,
		TotalProcessTime: m.TotalProcessTime,
		AvgProcessTime:   m.AvgProcessTime,
		LastUpdate:       m.LastUpdate,
	}
}

func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TasksProcessed = 0
	m.TasksCompleted = 0
	m.TasksFailed = 0
	m.TasksRetried = 0
	m.TotalProcessTime = 0
	m.AvgProcessTime = 0
	m.LastUpdate = time.Now()
}
