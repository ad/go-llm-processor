package poller

import (
	"context"

	"github.com/ad/llm-proxy/processor/internal/config"
	"github.com/ad/llm-proxy/processor/internal/worker"
)

type SseTaskSource struct {
	sseClient *worker.SSEClient
}

func NewSseTaskSource(cfg *config.Config) *SseTaskSource {
	sseClient := worker.NewSSEClient(
		cfg.WorkerURL,
		cfg.InternalAPIKey,
		cfg.ProcessorID,
		worker.SSEConfig{
			Enabled:              cfg.SSE.Enabled,
			Endpoint:             cfg.SSE.Endpoint,
			ReconnectInterval:    cfg.SSE.ReconnectInterval,
			MaxReconnectAttempts: cfg.SSE.MaxReconnectAttempts,
			HeartbeatTimeout:     cfg.SSE.HeartbeatTimeout,
			HeartbeatInterval:    cfg.SSE.HeartbeatInterval,
			MaxDuration:          cfg.SSE.MaxDuration,
		},
	)
	return &SseTaskSource{sseClient: sseClient}
}

func (s *SseTaskSource) Start(ctx context.Context, handler TaskHandler) {
	s.sseClient.Start(ctx)
	defer s.sseClient.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case taskData := <-s.sseClient.GetTaskChannel():
			// log.Printf("Received task via SSE: %s\n", taskData.TaskID)
			task := worker.Task{
				ID:           taskData.TaskID,
				Priority:     taskData.Priority,
				RetryCount:   taskData.RetryCount,
				ProductData:  taskData.ProductData,
				OllamaParams: taskData.OllamaParams,
				// ProductData и OllamaParams могут быть пустыми, если их нет в SSE
			}
			handler(ctx, task)
		}
	}
}
