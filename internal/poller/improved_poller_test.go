package poller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	"github.com/ad/llm-proxy/processor/internal/worker"
	"github.com/ad/llm-proxy/processor/pkg/ollama"
	workerpool "github.com/ad/llm-proxy/processor/pkg/worker"
)

// --- Моки ---

type MockWorkerClient struct {
	calls               map[string]int
	callsMu             sync.Mutex
	claimTasksBatchFunc func(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error)
	completeTaskFunc    func(ctx context.Context, id, procID, status, result, errMsg string) error
	requeueTaskFunc     func(ctx context.Context, id, procID, reason string) error
	releaseTaskFunc     func(ctx context.Context, id string) error
}

func (m *MockWorkerClient) CompleteTask(ctx context.Context, id, procID, status, result, errMsg string) error {
	m.callsMu.Lock()
	m.calls["CompleteTask"]++
	m.callsMu.Unlock()
	if m.completeTaskFunc != nil {
		return m.completeTaskFunc(ctx, id, procID, status, result, errMsg)
	}
	return nil
}
func (m *MockWorkerClient) RequeueTask(ctx context.Context, id, procID, reason string) error {
	m.callsMu.Lock()
	m.calls["RequeueTask"]++
	m.callsMu.Unlock()
	if m.requeueTaskFunc != nil {
		return m.requeueTaskFunc(ctx, id, procID, reason)
	}
	return nil
}
func (m *MockWorkerClient) ReleaseTask(ctx context.Context, id string) error {
	m.callsMu.Lock()
	m.calls["ReleaseTask"]++
	m.callsMu.Unlock()
	if m.releaseTaskFunc != nil {
		return m.releaseTaskFunc(ctx, id)
	}
	return nil
}
func (m *MockWorkerClient) ClaimTasksBatch(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error) {
	m.callsMu.Lock()
	m.calls["ClaimTasksBatch"]++
	m.callsMu.Unlock()
	if m.claimTasksBatchFunc != nil {
		return m.claimTasksBatchFunc(ctx, procID, batchSize, timeoutMs)
	}
	return nil, nil
}
func (m *MockWorkerClient) SendHeartbeat(ctx context.Context, taskID, procID string) error {
	return nil
}
func (m *MockWorkerClient) SendProcessorHeartbeat(ctx context.Context, procID string, cpu, mem *float64, queue *int) error {
	return nil
}
func (m *MockWorkerClient) TriggerCleanup(ctx context.Context) error { return nil }

// ---
type MockOllamaClient struct {
	calls                    map[string]int
	ensureModelAvailableFunc func(model string) error
	generateWithParamsFunc   func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error)
}

func (m *MockOllamaClient) EnsureModelAvailable(model string) error {
	m.calls["EnsureModelAvailable"]++
	if m.ensureModelAvailableFunc != nil {
		return m.ensureModelAvailableFunc(model)
	}
	return nil
}
func (m *MockOllamaClient) GenerateWithParams(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
	m.calls["GenerateWithParams"]++
	if m.generateWithParamsFunc != nil {
		return m.generateWithParamsFunc(ctx, model, productData, params)
	}
	return "", nil
}

// --- Интерфейсы для внедрения зависимостей ---
type IWorkerClient interface {
	CompleteTask(ctx context.Context, id, procID, status, result, errMsg string) error
	RequeueTask(ctx context.Context, id, procID, reason string) error
	ReleaseTask(ctx context.Context, id string) error
	ClaimTasksBatch(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error)
	SendHeartbeat(ctx context.Context, taskID, procID string) error
	SendProcessorHeartbeat(ctx context.Context, procID string, cpu, mem *float64, queue *int) error
	TriggerCleanup(ctx context.Context) error
}

type IOllamaClient interface {
	EnsureModelAvailable(model string) error
	GenerateWithParams(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error)
}

// --- Тест: успешная обработка задачи ---
func TestImprovedPoller_TaskSuccess(t *testing.T) {
	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	task := worker.Task{ID: "task1", ProductData: "data"}
	workerMock.claimTasksBatchFunc = func(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error) {
		return []worker.Task{task}, nil
	}
	ollamaMock.ensureModelAvailableFunc = func(model string) error {
		if model != "model1" {
			t.Errorf("unexpected model: %s", model)
		}
		return nil
	}
	ollamaMock.generateWithParamsFunc = func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
		if model != "model1" || productData != "data" {
			t.Errorf("unexpected params: %s %s", model, productData)
		}
		return "result", nil
	}
	workerMock.completeTaskFunc = func(ctx context.Context, id, procID, status, result, errMsg string) error {
		if id != "task1" || procID != "proc1" || status != "completed" || result != "result" {
			t.Errorf("unexpected CompleteTask params: %s %s %s %s", id, procID, status, result)
		}
		return nil
	}

	cfg := &config.Config{
		WorkerCount:       1,
		QueueSize:         1,
		MaxBatchSize:      1,
		ProcessorID:       "proc1",
		RequestTimeout:    time.Second,
		PollInterval:      10 * time.Millisecond,
		HeartbeatInterval: time.Second,
		MaxRetries:        1,
		ModelName:         "model1",
		InitialDelay:      0,
	}

	poller := &ImprovedPoller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:  make(map[string]string),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go poller.Start(ctx)

	// Ждём вызова CompleteTask с таймаутом
	ok := false
	for i := 0; i < 100; i++ {
		workerMock.callsMu.Lock()
		if workerMock.calls["CompleteTask"] > 0 {
			ok = true
		}
		workerMock.callsMu.Unlock()
		if ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok {
		t.Error("CompleteTask was not called")
	}
}

func TestImprovedPoller_TaskRequeueOnModelUnavailable(t *testing.T) {
	// Проверяет, что при недоступности модели вызывается RequeueTask, а CompleteTask не вызывается
}

func TestImprovedPoller_TaskRequeueOnGenerationError(t *testing.T) {
	// Проверяет, что при ошибке генерации после всех попыток вызывается RequeueTask
}

func TestImprovedPoller_ReleaseTaskOnPoolOverflow(t *testing.T) {
	// Проверяет, что при переполнении пула задача возвращается в очередь (ReleaseTask)
}

func TestImprovedPoller_BatchAndSingleMode(t *testing.T) {
	// Проверяет работу batch-режима и одиночного режима (batchSize=1)
}

func TestImprovedPoller_HeartbeatWithAndWithoutMetrics(t *testing.T) {
	// Проверяет отправку heartbeat по задачам и heartbeat с метриками (если включено)
}

func TestImprovedPoller_OnDoneCallback(t *testing.T) {
	// Проверяет, что callback OnDone вызывается при любом завершении задачи
	// ...инициализация ImprovedTaskJob с этим callback и запуск...
	// assert.Equal(t, 1, called)
}

func TestImprovedPoller_WithTaskSource(t *testing.T) {
	// Проверяет работу ImprovedPoller с внешним TaskSource (push-модель)
}

// Для каждого теста нужно реализовать моки и проверки вызовов методов workerClient, ollimaClient и т.д.
// Это заготовка для быстрой реализации тестов и контроля регрессий при рефакторинге ImprovedPoller.
