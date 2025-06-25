package poller

import (
	"context"

	"github.com/ad/llm-proxy/processor/internal/worker"
)

type TaskHandler func(ctx context.Context, task worker.Task)

type TaskSource interface {
	Start(ctx context.Context, handler TaskHandler)
}
