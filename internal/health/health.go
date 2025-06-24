package health

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ad/llm-proxy/processor/pkg/metrics"
)

type Status struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Service   string    `json:"service"`
}

func NewHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/ready", handleReady)
	mux.HandleFunc("/metrics", handleMetrics)

	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	status := Status{
		Status:    "healthy",
		Timestamp: time.Now(),
		Service:   "llm-proxy-processor",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func handleReady(w http.ResponseWriter, r *http.Request) {
	status := Status{
		Status:    "ready",
		Timestamp: time.Now(),
		Service:   "llm-proxy-processor",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	metricsSnapshot := metrics.GlobalMetrics.GetSnapshot()

	response := map[string]interface{}{
		"metrics": map[string]interface{}{
			"tasks_processed":     metricsSnapshot.TasksProcessed,
			"tasks_completed":     metricsSnapshot.TasksCompleted,
			"tasks_failed":        metricsSnapshot.TasksFailed,
			"tasks_retried":       metricsSnapshot.TasksRetried,
			"avg_process_time_ms": metricsSnapshot.AvgProcessTime.Milliseconds(),
			"last_update":         metricsSnapshot.LastUpdate,
		},
		"timestamp": time.Now(),
		"service":   "llm-proxy-processor",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
