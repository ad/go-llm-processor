package poller

import (
	"context"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	"github.com/ad/llm-proxy/processor/internal/worker"
)

type PollingTaskSource struct {
	workerClient *worker.Client
	cfg          *config.Config
}

func NewPollingTaskSource(workerClient *worker.Client, cfg *config.Config) *PollingTaskSource {
	return &PollingTaskSource{workerClient: workerClient, cfg: cfg}
}

func (p *PollingTaskSource) Start(ctx context.Context, handler TaskHandler) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tasks, err := p.workerClient.GetPendingTasks(ctx)
			if err != nil || len(tasks) == 0 {
				continue
			}
			for _, task := range tasks {
				handler(ctx, task)
			}
		}
	}
}
