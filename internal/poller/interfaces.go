package poller

import (
	"context"

	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/ollama"
)

type WorkerClient interface {
	CompleteTask(ctx context.Context, id, procID, status, result, errMsg string) error
	RequeueTask(ctx context.Context, id, procID, reason string) error
	ReleaseTask(ctx context.Context, id string) error
	ClaimTasksBatch(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error)
	SendHeartbeat(ctx context.Context, taskID, procID string) error
	SendProcessorHeartbeat(ctx context.Context, procID string, cpu, mem *float64, queue *int) error
	TriggerCleanup(ctx context.Context) error
}

type OllamaClient interface {
	EnsureModelAvailable(model string) error
	GenerateWithParams(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error)
}
