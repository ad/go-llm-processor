//go:build goexperiment.synctest

package metrics

import (
	"testing"
	"testing/synctest"
	"time"
)

// TestMetrics_ConcurrencyWithSynctest demonstrates Go 1.24's synctest for deterministic concurrent testing
func TestMetrics_ConcurrencyWithSynctest(t *testing.T) {
	synctest.Run(func() {
		m := &Metrics{}

		// Create multiple goroutines that increment metrics
		for i := 0; i < 5; i++ {
			go func(id int) {
				for j := 0; j < 20; j++ {
					m.IncrementProcessed()
					m.IncrementCompleted(time.Duration(id+1) * time.Millisecond)
				}
			}(i)
		}

		// synctest ensures deterministic execution
		snapshot := m.GetSnapshot()

		// All operations should complete deterministically
		if snapshot.TasksProcessed != 100 {
			t.Errorf("Expected TasksProcessed=100, got %d", snapshot.TasksProcessed)
		}
		if snapshot.TasksCompleted != 100 {
			t.Errorf("Expected TasksCompleted=100, got %d", snapshot.TasksCompleted)
		}

		// Average should be calculated correctly
		// (1+2+3+4+5)*20 / 100 = 300/100 = 3ms
		expectedAvg := 3 * time.Millisecond
		if snapshot.AvgProcessTime != expectedAvg {
			t.Errorf("Expected AvgProcessTime=%v, got %v", expectedAvg, snapshot.AvgProcessTime)
		}
	})
}

// TestMetrics_RaceConditionDetection demonstrates race condition detection with synctest
func TestMetrics_RaceConditionDetection(t *testing.T) {
	synctest.Run(func() {
		m := &Metrics{}

		// This test ensures no race conditions in concurrent access
		done := make(chan bool, 2)

		// Reader goroutine
		go func() {
			for i := 0; i < 50; i++ {
				_ = m.GetSnapshot()
			}
			done <- true
		}()

		// Writer goroutine
		go func() {
			for i := 0; i < 50; i++ {
				m.IncrementProcessed()
				m.IncrementCompleted(time.Millisecond)
			}
			done <- true
		}()

		// Wait for both goroutines
		<-done
		<-done

		snapshot := m.GetSnapshot()
		if snapshot.TasksProcessed != 50 {
			t.Errorf("Expected TasksProcessed=50, got %d", snapshot.TasksProcessed)
		}
	})
}
