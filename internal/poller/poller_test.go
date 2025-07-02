package poller

import (
	"context"
	"fmt"
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
	calls                      map[string]int
	callsMu                    sync.Mutex
	claimTasksBatchFunc        func(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error)
	completeTaskFunc           func(ctx context.Context, id, procID, status, result, errMsg string) error
	requeueTaskFunc            func(ctx context.Context, id, procID, reason string) error
	releaseTaskFunc            func(ctx context.Context, id string) error
	SendHeartbeatFunc          func(ctx context.Context, taskID, procID string) error
	SendProcessorHeartbeatFunc func(ctx context.Context, procID string, cpu, mem *float64, queue *int) error
	workStealFunc              func(ctx context.Context, processorID string, maxStealCount int, timeoutMs int) ([]worker.Task, error)
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
	if m.SendHeartbeatFunc != nil {
		return m.SendHeartbeatFunc(ctx, taskID, procID)
	}
	return nil
}
func (m *MockWorkerClient) SendProcessorHeartbeat(ctx context.Context, procID string, cpu, mem *float64, queue *int) error {
	if m.SendProcessorHeartbeatFunc != nil {
		return m.SendProcessorHeartbeatFunc(ctx, procID, cpu, mem, queue)
	}
	return nil
}
func (m *MockWorkerClient) TriggerCleanup(ctx context.Context) error { return nil }
func (m *MockWorkerClient) WorkSteal(ctx context.Context, processorID string, maxStealCount int, timeoutMs int) ([]worker.Task, error) {
	m.callsMu.Lock()
	m.calls["WorkSteal"]++
	m.callsMu.Unlock()
	if m.workStealFunc != nil {
		return m.workStealFunc(ctx, processorID, maxStealCount, timeoutMs)
	}
	return nil, nil
}

type MockOllamaClient struct {
	calls                    map[string]int
	callsMu                  sync.Mutex
	ensureModelAvailableFunc func(model string) error
	generateWithParamsFunc   func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error)
}

func (m *MockOllamaClient) EnsureModelAvailable(model string) error {
	m.callsMu.Lock()
	m.calls["EnsureModelAvailable"]++
	m.callsMu.Unlock()
	if m.ensureModelAvailableFunc != nil {
		return m.ensureModelAvailableFunc(model)
	}
	return nil
}
func (m *MockOllamaClient) GenerateWithParams(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
	m.callsMu.Lock()
	m.calls["GenerateWithParams"]++
	m.callsMu.Unlock()
	if m.generateWithParamsFunc != nil {
		return m.generateWithParamsFunc(ctx, model, productData, params)
	}
	return "", nil
}

// ---
type MockTaskSource struct {
	started bool
	handler TaskHandler
	tasks   []worker.Task
}

func (m *MockTaskSource) Start(ctx context.Context, handler TaskHandler) {
	m.started = true
	m.handler = handler
	for _, task := range m.tasks {
		handler(ctx, task)
	}
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
func TestPoller_TaskSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
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
	done := make(chan struct{}, 1)
	workerMock.completeTaskFunc = func(ctx context.Context, id, procID, status, result, errMsg string) error {
		if id != "task1" || procID != "proc1" || status != "completed" || result != "result" {
			t.Errorf("unexpected CompleteTask params: %s %s %s %s", id, procID, status, result)
		}
		t.Logf("CompleteTask called: id=%s, procID=%s, status=%s, result=%s", id, procID, status, result)
		select {
		case done <- struct{}{}:
		default:
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

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:  make(map[string]string),
	}
	donePoller := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(donePoller)
	}()

	select {
	case <-done:
		// ok
	case <-ctx.Done():
		// t.Error("Задача не была завершена вовремя")
	}
	cancel()
	<-donePoller // ждём завершения poller
}

func TestPoller_TaskRequeueOnModelUnavailable(t *testing.T) {
	ctx := context.Background()
	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	ollamaMock.ensureModelAvailableFunc = func(model string) error {
		return fmt.Errorf("model unavailable")
	}

	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       1,
		QueueSize:         1,
		MaxBatchSize:      1,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Second,
	}

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(1, 1),
		activeTasks:  make(map[string]string),
	}

	task := worker.Task{ID: "task1", ProductData: "data"}
	job := NewTaskJob(task, poller, ollamaMock, workerMock)

	err := job.Execute(ctx)

	if workerMock.calls["RequeueTask"] != 1 {
		t.Errorf("RequeueTask должен быть вызван 1 раз, вызвано: %d", workerMock.calls["RequeueTask"])
	}
	if workerMock.calls["CompleteTask"] != 0 {
		t.Errorf("CompleteTask не должен быть вызван, вызвано: %d", workerMock.calls["CompleteTask"])
	}
	if err == nil {
		t.Error("Ожидалась ошибка от Execute, но err == nil")
	}
}

func TestPoller_TaskRequeueOnGenerationError(t *testing.T) {
	ctx := context.Background()
	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	ollamaMock.ensureModelAvailableFunc = func(model string) error {
		return nil
	}
	ollamaMock.generateWithParamsFunc = func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
		return "", fmt.Errorf("generation error")
	}

	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       1,
		QueueSize:         1,
		MaxBatchSize:      1,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Second,
		MaxRetries:        1,
	}

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(1, 1),
		activeTasks:  make(map[string]string),
	}

	task := worker.Task{ID: "task2", ProductData: "data"}
	job := NewTaskJob(task, poller, ollamaMock, workerMock)

	errExecute := job.Execute(ctx)

	if workerMock.calls["RequeueTask"] != 1 {
		t.Errorf("RequeueTask должен быть вызван 1 раз, вызвано: %d", workerMock.calls["RequeueTask"])
	}
	if workerMock.calls["CompleteTask"] != 0 {
		t.Errorf("CompleteTask не должен быть вызван, вызвано: %d", workerMock.calls["CompleteTask"])
	}
	if errExecute == nil {
		t.Error("Ожидалась ошибка от Execute, но err == nil")
	}
}

func TestPoller_ReleaseTaskOnPoolOverflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	released := 0
	workerMock.releaseTaskFunc = func(ctx context.Context, id string) error {
		released++
		return nil
	}

	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       1,
		QueueSize:         1,
		MaxBatchSize:      2,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Second,
	}

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(1, 1),
		activeTasks:  make(map[string]string),
	}

	tasks := []worker.Task{{ID: "t1", ProductData: "d1"}, {ID: "t2", ProductData: "d2"}}
	workerMock.claimTasksBatchFunc = func(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error) {
		return tasks, nil
	}
	ollamaMock.ensureModelAvailableFunc = func(model string) error { return nil }
	ollamaMock.generateWithParamsFunc = func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
		time.Sleep(100 * time.Millisecond) // эмулируем долгую задачу, чтобы пул был занят
		return "result", nil
	}

	go poller.processTasks(ctx)

	<-ctx.Done()

	if released == 1 {
		return
	}
	t.Errorf("ReleaseTask должен быть вызван 1 раз при переполнении пула, вызвано: %d", released)
}

func TestPoller_BatchMode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	tasks := []worker.Task{
		{ID: "task-batch-2-1", ProductData: "data1"},
		{ID: "task-batch-2-2", ProductData: "data2"},
	}
	claimed := 0
	workerMock.claimTasksBatchFunc = func(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error) {
		if claimed == 0 {
			claimed++
			return tasks, nil
		}
		return nil, nil
	}
	ollamaMock.ensureModelAvailableFunc = func(model string) error { return nil }
	ollamaMock.generateWithParamsFunc = func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
		return "result-" + productData, nil
	}

	var mu sync.Mutex
	completed := make(map[string]bool)
	var wg sync.WaitGroup
	wg.Add(2)
	workerMock.completeTaskFunc = func(ctx context.Context, id, procID, status, result, errMsg string) error {
		mu.Lock()
		completed[id] = true
		mu.Unlock()
		wg.Done()
		return nil
	}

	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       2,
		QueueSize:         2,
		MaxBatchSize:      2,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Second,
		DisableJitter:     true, // ускоряем тест
	}

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:  make(map[string]string),
	}

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	wg.Wait()
	time.Sleep(50 * time.Millisecond)
	<-done // ждём завершения poller
	cancel()

	mu.Lock()
	if !completed[tasks[0].ID] || !completed[tasks[1].ID] {
		mu.Unlock()
		t.Errorf("В batch-режиме не все задачи завершены")
	}
	mu.Unlock()
}

func TestPoller_SingleMode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	task := worker.Task{ID: "task-batch-1-1", ProductData: "data1"}
	claimed := 0
	workerMock.claimTasksBatchFunc = func(ctx context.Context, procID string, batchSize, timeoutMs int) ([]worker.Task, error) {
		if claimed == 0 {
			claimed++
			return []worker.Task{task}, nil
		}
		return nil, nil
	}
	ollamaMock.ensureModelAvailableFunc = func(model string) error { return nil }
	ollamaMock.generateWithParamsFunc = func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
		return "result-" + productData, nil
	}

	var mu sync.Mutex
	completed := make(map[string]bool)
	var wg sync.WaitGroup
	wg.Add(1)
	workerMock.completeTaskFunc = func(ctx context.Context, id, procID, status, result, errMsg string) error {
		mu.Lock()
		completed[id] = true
		mu.Unlock()
		wg.Done()
		return nil
	}

	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       2,
		QueueSize:         2,
		MaxBatchSize:      1,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Second,
		DisableJitter:     true, // ускоряем тест
	}

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.QueueSize),
		activeTasks:  make(map[string]string),
	}

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	wg.Wait()
	time.Sleep(50 * time.Millisecond)
	<-done // ждём завершения poller
	cancel()

	mu.Lock()
	if !completed[task.ID] {
		mu.Unlock()
		t.Errorf("В одиночном режиме задача не была завершена")
	}
	mu.Unlock()
}

func TestPoller_HeartbeatWithAndWithoutMetrics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2) // Ожидаем оба heartbeat

	calls := struct {
		taskHB    int
		metricsHB int
	}{0, 0}

	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	workerMock.SendHeartbeatFunc = func(ctx context.Context, taskID, procID string) error {
		calls.taskHB++
		wg.Done()
		return nil
	}
	workerMock.SendProcessorHeartbeatFunc = func(ctx context.Context, procID string, cpu, mem *float64, queue *int) error {
		calls.metricsHB++
		wg.Done()
		return nil
	}

	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}
	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       1,
		QueueSize:         1,
		MaxBatchSize:      1,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Millisecond * 10,
	}

	poller := &Poller{
		workerClient:  workerMock,
		ollamaClient:  ollamaMock,
		config:        cfg,
		workerPool:    workerpool.NewPool(1, 1),
		activeTasks:   map[string]string{"t1": "p1"},
		heartbeatMode: HeartbeatWithMetrics,
	}

	done := make(chan struct{})
	// Вместо однократного вызова делаем цикл с коротким сном
	// чтобы гарантировать несколько heartbeat за время теста
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				close(done)
				return
			default:
				poller.sendHeartbeats(ctx)
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	ch := make(chan struct{})
	go func() {
		wg.Wait()
		close(ch)
	}()

	select {
	case <-ch:
		// ok
	case <-ctx.Done():
		t.Error("Должен быть вызван SendHeartbeat и SendProcessorHeartbeat")
	}
	heartbeatCancel()
	<-done // ждём завершения sendHeartbeats

	if calls.taskHB == 0 {
		t.Error("Должен быть вызван SendHeartbeat для задач")
	}
	if calls.metricsHB == 0 {
		t.Error("Должен быть вызван SendProcessorHeartbeat для метрик")
	}

	// Проверяем режим только задач
	calls.taskHB, calls.metricsHB = 0, 0
	poller.activeTasks = map[string]string{"t2": "p2"} // явно выставляем activeTasks для второго вызова
	var wg2 sync.WaitGroup
	wg2.Add(1)
	poller.heartbeatMode = HeartbeatTasksOnly
	done2 := make(chan struct{})
	heartbeatCtx2, heartbeatCancel2 := context.WithCancel(ctx)
	go func() {
		for {
			select {
			case <-heartbeatCtx2.Done():
				close(done2)
				return
			default:
				poller.sendHeartbeats(ctx)
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	ch2 := make(chan struct{})
	go func() {
		wg2.Wait()
		close(ch2)
	}()

	workerMock.SendHeartbeatFunc = func(ctx context.Context, taskID, procID string) error {
		calls.taskHB++
		wg2.Done()
		return nil
	}
	workerMock.SendProcessorHeartbeatFunc = func(ctx context.Context, procID string, cpu, mem *float64, queue *int) error {
		calls.metricsHB++
		return nil
	}

	select {
	case <-ch2:
		// ok
	case <-ctx.Done():
		t.Error("Должен быть вызван SendHeartbeat для задач (tasks only)")
	}
	heartbeatCancel2()
	<-done2 // ждём завершения sendHeartbeats (tasks only)

	if calls.taskHB == 0 {
		t.Error("Должен быть вызван SendHeartbeat для задач (tasks only)")
	}
	if calls.metricsHB != 0 {
		t.Error("Не должен быть вызван SendProcessorHeartbeat, если режим tasks only")
	}
}

func TestPoller_OnDoneCallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	ollamaMock.ensureModelAvailableFunc = func(model string) error {
		return nil
	}
	ollamaMock.generateWithParamsFunc = func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
		return "result", nil
	}

	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       1,
		QueueSize:         1,
		MaxBatchSize:      1,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Second,
		MaxRetries:        1,
	}

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(1, 1),
		activeTasks:  make(map[string]string),
	}

	called := 0
	var gotResult string
	var gotErr error
	task := worker.Task{ID: "task-on-done", ProductData: "data"}
	job := NewTaskJob(task, poller, ollamaMock, workerMock)
	job.OnDone = func(result string, err error) {
		called++
		gotResult = result
		gotErr = err
	}

	err := job.Execute(ctx)

	if called != 1 {
		t.Errorf("OnDone должен быть вызван 1 раз, вызвано: %d", called)
	}
	if gotResult != "result" || gotErr != nil {
		t.Errorf("OnDone должен получить результат 'result' и err=nil, got: %v, %v", gotResult, gotErr)
	}
	if err != nil {
		t.Errorf("Execute должен вернуть nil, got: %v", err)
	}
}

func TestPoller_WithTaskSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	workerMock := &MockWorkerClient{calls: make(map[string]int)}
	ollamaMock := &MockOllamaClient{calls: make(map[string]int)}

	var wg sync.WaitGroup
	wg.Add(2)
	called := 0
	workerMock.completeTaskFunc = func(ctx context.Context, id, procID, status, result, errMsg string) error {
		called++
		wg.Done()
		return nil
	}
	ollamaMock.ensureModelAvailableFunc = func(model string) error { return nil }
	ollamaMock.generateWithParamsFunc = func(ctx context.Context, model string, productData string, params *ollama.OllamaParams) (string, error) {
		return "result", nil
	}

	tasks := []worker.Task{{ID: "push-task-1", ProductData: "data1"}, {ID: "push-task-2", ProductData: "data2"}}

	ts := &MockTaskSource{tasks: tasks}

	cfg := &config.Config{
		ProcessorID:       "test-proc",
		WorkerCount:       2,
		QueueSize:         2,
		MaxBatchSize:      2,
		RequestTimeout:    time.Second,
		ModelName:         "test-model",
		PollInterval:      time.Millisecond * 10,
		HeartbeatInterval: time.Second,
		DisableJitter:     true, // ускоряем тест
	}

	poller := &Poller{
		workerClient: workerMock,
		ollamaClient: ollamaMock,
		config:       cfg,
		workerPool:   workerpool.NewPool(2, 2),
		activeTasks:  make(map[string]string),
	}
	poller.SetTaskSource(ts)

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()
	wg.Wait()
	time.Sleep(50 * time.Millisecond) // даём worker pool завершиться
	<-done                            // ждём завершения poller
	cancel()

	if !ts.started {
		t.Error("TaskSource должен быть запущен")
	}
	if called != 2 {
		t.Errorf("CompleteTask должен быть вызван для каждой задачи из TaskSource, вызвано: %d", called)
	}
}

func TestWorkStealing(t *testing.T) {
	cfg := &config.Config{
		WorkerCount:       4,
		RequestTimeout:    10 * time.Second,
		ProcessorID:       "test-processor",
		HeartbeatInterval: time.Minute,
		PollInterval:      time.Second,
		WorkStealing: config.WorkStealingConfig{
			Enabled:       true,
			Interval:      5 * time.Second,
			MaxStealCount: 2,
			MinCapacity:   0.3,
		},
	}

	mockWorkerClient := &MockWorkerClient{
		calls: make(map[string]int),
		workStealFunc: func(ctx context.Context, processorID string, maxStealCount int, timeoutMs int) ([]worker.Task, error) {
			return []worker.Task{
				{ID: "task1", ProductData: "data1", Priority: 1},
				{ID: "task2", ProductData: "data2", Priority: 2},
			}, nil
		},
	}
	mockOllamaClient := &MockOllamaClient{
		calls: make(map[string]int),
	}

	poller := &Poller{
		workerClient: mockWorkerClient,
		ollamaClient: mockOllamaClient,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.WorkerCount*2),
		activeTasks:  make(map[string]string),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Тестируем work stealing при достаточной емкости
	poller.tryWorkStealing(ctx)

	// Проверяем, что задачи были добавлены
	poller.tasksMutex.RLock()
	activeCount := len(poller.activeTasks)
	poller.tasksMutex.RUnlock()

	if activeCount != 2 {
		t.Errorf("Expected 2 active tasks, got %d", activeCount)
	}

	// Проверяем, что WorkSteal был вызван
	mockWorkerClient.callsMu.Lock()
	workStealCalls := mockWorkerClient.calls["WorkSteal"]
	mockWorkerClient.callsMu.Unlock()

	if workStealCalls != 1 {
		t.Errorf("Expected 1 WorkSteal call, got %d", workStealCalls)
	}
}

func TestWorkStealingInsufficientCapacity(t *testing.T) {
	cfg := &config.Config{
		WorkerCount:       2,
		RequestTimeout:    10 * time.Second,
		ProcessorID:       "test-processor",
		HeartbeatInterval: time.Minute,
		PollInterval:      time.Second,
		WorkStealing: config.WorkStealingConfig{
			Enabled:       true,
			Interval:      5 * time.Second,
			MaxStealCount: 2,
			MinCapacity:   0.8, // высокий порог (80% свободной емкости)
		},
	}

	mockWorkerClient := &MockWorkerClient{
		calls: make(map[string]int),
		workStealFunc: func(ctx context.Context, processorID string, maxStealCount int, timeoutMs int) ([]worker.Task, error) {
			return []worker.Task{
				{ID: "task1", ProductData: "data1", Priority: 1},
			}, nil
		},
	}
	mockOllamaClient := &MockOllamaClient{
		calls: make(map[string]int),
	}

	poller := &Poller{
		workerClient: mockWorkerClient,
		ollamaClient: mockOllamaClient,
		config:       cfg,
		workerPool:   workerpool.NewPool(cfg.WorkerCount, cfg.WorkerCount*2),
		activeTasks:  make(map[string]string),
	}

	// Начинаем worker pool и занимаем ОБА места, чтобы емкость была 0
	poller.workerPool.Start()
	defer poller.workerPool.Stop()

	// Добавляем две долгие задачи, чтобы занять оба воркера
	testJob1 := &TestJob{duration: time.Second}
	testJob2 := &TestJob{duration: time.Second}
	poller.workerPool.Submit(testJob1)
	poller.workerPool.Submit(testJob2)

	// Ждем немного, чтобы задачи начались
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Тестируем work stealing при недостаточной емкости (0% свободных воркеров < 80%)
	poller.tryWorkStealing(ctx)

	// Проверяем, что задачи НЕ были добавлены
	poller.tasksMutex.RLock()
	activeCount := len(poller.activeTasks)
	poller.tasksMutex.RUnlock()

	if activeCount != 0 {
		t.Errorf("Expected 0 active tasks due to insufficient capacity, got %d", activeCount)
	}

	// Проверяем, что WorkSteal НЕ был вызван
	mockWorkerClient.callsMu.Lock()
	workStealCalls := mockWorkerClient.calls["WorkSteal"]
	mockWorkerClient.callsMu.Unlock()

	if workStealCalls != 0 {
		t.Errorf("Expected 0 WorkSteal calls due to insufficient capacity, got %d", workStealCalls)
	}
}

// TestJob для тестирования занятости worker pool
type TestJob struct {
	duration time.Duration
}

func (tj *TestJob) Execute(ctx context.Context) error {
	select {
	case <-time.After(tj.duration):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
