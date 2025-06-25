package poller

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ad/llm-proxy/processor/internal/ollama"
	"github.com/ad/llm-proxy/processor/internal/promptutils"
	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/metrics"
	"github.com/ad/llm-proxy/processor/pkg/retry"
)

type TaskJob struct {
	task         worker.Task
	poller       *Poller
	ollamaClient *ollama.Client
	workerClient *worker.Client
}

func NewTaskJob(task worker.Task, poller *Poller, ollamaClient *ollama.Client, workerClient *worker.Client) *TaskJob {
	return &TaskJob{
		task:         task,
		poller:       poller,
		ollamaClient: ollamaClient,
		workerClient: workerClient,
	}
}

func (tj *TaskJob) Execute(ctx context.Context) error {
	startTime := time.Now()
	log.Printf("Processing task %s with processor %s", tj.task.ID, tj.poller.config.ProcessorID)

	// Ensure task is removed from active list on completion
	defer tj.poller.removeActiveTask(tj.task.ID)

	metrics.GlobalMetrics.IncrementProcessed()

	// Parse ollama parameters from task
	ollamaParams, err := tj.task.ParseOllamaParams()
	if err != nil {
		log.Printf("Error parsing ollama params for task %s: %v, using defaults", tj.task.ID, err)
		ollamaParams = nil
	}

	// Create timeout context for Ollama generation
	taskCtx, cancel := context.WithTimeout(ctx, tj.poller.config.RequestTimeout)
	defer cancel()

	var result string

	// Use retry mechanism for Ollama generation
	retryConfig := retry.DefaultConfig()
	retryConfig.MaxRetries = tj.poller.config.MaxRetries

	err = retry.Do(taskCtx, retryConfig, func() error {
		var genErr error

		// Generate prompt for product description
		var prompt string
		if ollamaParams != nil {
			prompt = promptutils.BuildPromptFromString(tj.task.ProductData, ollamaParams.Prompt)
		} else {
			prompt = promptutils.BuildPromptFromString(tj.task.ProductData, "")
		}

		modelToUse := tj.poller.config.ModelName
		// Use custom parameters if provided, otherwise use defaults
		if ollamaParams != nil {
			// Use custom model if specified, otherwise use config default
			if ollamaParams.Model != "" {
				modelToUse = ollamaParams.Model
			}
		} else {
			temp := 0.3
			topP := 0.9
			topK := 40
			repeatPenalty := 1.1

			ollamaParams = &ollama.OllamaParams{
				Model:         modelToUse,
				Temperature:   &temp,
				TopP:          &topP,
				TopK:          &topK,
				RepeatPenalty: &repeatPenalty,
			}
		}

		result, genErr = tj.ollamaClient.GenerateWithParams(taskCtx, modelToUse, prompt, ollamaParams)

		if genErr != nil {
			metrics.GlobalMetrics.IncrementRetried()
		}
		return genErr
	})

	if err != nil {
		log.Printf("Error generating description for task %s after retries: %v", tj.task.ID, err)
		metrics.GlobalMetrics.IncrementFailed()
		// Возврат задачи в пул через requeue
		requeueErr := tj.workerClient.RequeueTask(ctx, tj.task.ID, tj.poller.config.ProcessorID, fmt.Sprintf("ollama error: %v", err))
		if requeueErr != nil {
			log.Printf("[REQUEUE ERROR] Failed to requeue task %s: %v", tj.task.ID, requeueErr)
		}
		return tj.workerClient.CompleteTask(ctx, tj.task.ID, tj.poller.config.ProcessorID, "failed", "", fmt.Sprintf("Generation failed after retries: %v", err))
	}

	// Complete task with result
	if err := tj.workerClient.CompleteTask(ctx, tj.task.ID, tj.poller.config.ProcessorID, "completed", result, ""); err != nil {
		metrics.GlobalMetrics.IncrementFailed()
		return fmt.Errorf("complete task %s: %w", tj.task.ID, err)
	}

	processingTime := time.Since(startTime)
	metrics.GlobalMetrics.IncrementCompleted(processingTime)
	log.Printf("Task %s completed successfully by processor %s in %v", tj.task.ID, tj.poller.config.ProcessorID, processingTime)
	return nil
}
