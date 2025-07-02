package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/ollama"
)

// Демонстрационный клиент для work-stealing
type DemoWorkerClient struct{}

func (d *DemoWorkerClient) CompleteTask(ctx context.Context, id, procID, status, result, errMsg string) error {
	return nil
}

func (d *DemoWorkerClient) RequeueTask(ctx context.Context, id, procID, reason string) error {
	return nil
}

func (d *DemoWorkerClient) ReleaseTask(ctx context.Context, id string) error {
	return nil
}

func (d *DemoWorkerClient) ClaimTasksBatch(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error) {
	return nil, nil
}

func (d *DemoWorkerClient) SendHeartbeat(ctx context.Context, taskID, procID string) error {
	return nil
}

func (d *DemoWorkerClient) SendProcessorHeartbeat(ctx context.Context, procID string, cpu, mem *float64, queue *int) error {
	return nil
}

func (d *DemoWorkerClient) TriggerCleanup(ctx context.Context) error {
	return nil
}

func (d *DemoWorkerClient) WorkSteal(ctx context.Context, processorID string, maxStealCount int, timeoutMs int) ([]worker.Task, error) {
	fmt.Printf("🔄 WorkSteal called: processor=%s, maxCount=%d, timeout=%dms\n", processorID, maxStealCount, timeoutMs)

	// Симулируем успешное воровство одной задачи
	return []worker.Task{
		{
			ID:          "stolen-task-1",
			ProductData: "Stolen task data",
			Priority:    1,
		},
	}, nil
}

// Демонстрационный клиент Ollama
type DemoOllamaClient struct{}

func (d *DemoOllamaClient) EnsureModelAvailable(model string) error {
	return nil
}

func (d *DemoOllamaClient) GenerateWithParams(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
	return "Generated result for: " + productData, nil
}

func main() {
	fmt.Println("🚀 Work-Stealing Demo")
	fmt.Println("====================")

	// Создаем конфигурацию с включенным work-stealing
	cfg := &config.Config{
		WorkerCount:       2,
		RequestTimeout:    10 * time.Second,
		ProcessorID:       "demo-processor",
		HeartbeatInterval: time.Minute,
		PollInterval:      time.Second,
		WorkStealing: config.WorkStealingConfig{
			Enabled:       true,
			Interval:      3 * time.Second, // Короткий интервал для демо
			MaxStealCount: 2,
			MinCapacity:   0.3,
		},
	}

	fmt.Printf("📝 Конфигурация Work-Stealing:\n")
	fmt.Printf("   Enabled: %v\n", cfg.WorkStealing.Enabled)
	fmt.Printf("   Interval: %v\n", cfg.WorkStealing.Interval)
	fmt.Printf("   MaxStealCount: %d\n", cfg.WorkStealing.MaxStealCount)
	fmt.Printf("   MinCapacity: %.1f\n", cfg.WorkStealing.MinCapacity)
	fmt.Println()

	// Создаем клиенты
	workerClient := &DemoWorkerClient{}
	_ = &DemoOllamaClient{} // Используется в реальном коде

	fmt.Println("⚡ Запуск демонстрации work-stealing...")
	fmt.Println("Поллер будет пытаться украсть задачи каждые 3 секунды")
	fmt.Println("Нажмите Ctrl+C для остановки")
	fmt.Println()

	// Создаем контекст с таймаутом для демо
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Запускаем поллер в горутине
	go func() {
		// Здесь в реальном коде был бы pol.Start(ctx)
		// Для демо симулируем work-stealing вручную
		ticker := time.NewTicker(cfg.WorkStealing.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fmt.Printf("⏰ [%s] Попытка work-stealing...\n", time.Now().Format("15:04:05"))

				// Симулируем вызов WorkSteal
				stolenTasks, err := workerClient.WorkSteal(ctx, cfg.ProcessorID, cfg.WorkStealing.MaxStealCount, int(cfg.RequestTimeout.Milliseconds()))
				if err != nil {
					fmt.Printf("❌ Ошибка work-stealing: %v\n", err)
					continue
				}

				if len(stolenTasks) > 0 {
					fmt.Printf("✅ Успешно украдено %d задач:\n", len(stolenTasks))
					for _, task := range stolenTasks {
						fmt.Printf("   - %s: %s\n", task.ID, task.ProductData)
					}
				} else {
					fmt.Printf("ℹ️  Нет задач для воровства\n")
				}
				fmt.Println()
			}
		}
	}()

	// Ждем завершения
	<-ctx.Done()
	fmt.Println("\n🏁 Демонстрация завершена")
}
