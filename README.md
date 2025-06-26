# go-llm processor

## Описание
`processor` — сервис обработки задач платформы go-llm. Отвечает за взаимодействие с LLM (Ollama), worker pool, обработку очередей, SSE-уведомления и мониторинг.

## Ключевые особенности
- Асинхронная обработка задач из очереди
- Взаимодействие с Ollama через HTTP API (internal/ollama/client.go)
- Worker pool (pkg/worker/pool.go) с контролем параллелизма
- SSE-уведомления клиентам о статусе задач (internal/worker/sse_client.go)
- Poller для отслеживания новых задач (internal/poller/)
- Генерация prompt (internal/promptutils/prompt.go)
- Метрики и мониторинг (internal/metrics/)
- Чистая архитектура, явная обработка ошибок, dependency injection

## Структура проекта
```
processor/
├── cmd/processor/main.go      # Точка входа
├── internal/
│   ├── ollama/                # Клиент Ollama
│   ├── worker/                # SSE, обработка задач
│   ├── poller/                # Poller, очереди
│   ├── promptutils/           # Prompt builder
│   ├── metrics/               # Метрики
│   └── config/                # Конфиг
├── pkg/worker/                # Worker pool
├── pkg/metrics/               # Метрики
├── pkg/retry/                 # Повторные попытки
├── Dockerfile, Makefile       # Сборка и запуск
├── README.md                  # Документация
```

## Переменные окружения

| Переменная                | Назначение                                 | Значение по умолчанию         |
|---------------------------|--------------------------------------------|-------------------------------|
| OLLAMA_URL                | URL Ollama API                             | http://ollama:11434           |
| WORKER_URL                | URL для внутренних worker-API               | http://wrangler:8080          |
| HEALTH_ADDR               | Адрес для health endpoint                   | :8081                        |
| POLL_INTERVAL             | Интервал опроса задач         | 5s                            |
| POLLING_ENABLED           | Включить poller очереди                     | true                          |
| MAX_RETRIES               | Максимум попыток обработки задачи           | 3                             |
| REQUEST_TIMEOUT           | Таймаут HTTP-запроса к Ollama | 30s                           |
| MODEL_NAME                | Имя модели Ollama                           | gemma2:1b                     |
| WORKER_COUNT              | Количество воркеров                         | 3                             |
| QUEUE_SIZE                | Размер очереди задач                        | 100                           |
| PROCESSOR_ID              | Идентификатор процессора                    | proc-<random>                 |
| HEARTBEAT_INTERVAL        | Интервал heartbeat для задач  | 15s                           |
| MAX_BATCH_SIZE            | Максимальный batch для claim                | 5                             |
| INTERNAL_API_KEY          | Ключ для внутренних API                     | dev-internal-key              |
| SSE_ENABLED               | Включить SSE-клиент                         | true                          |
| SSE_ENDPOINT              | SSE endpoint для получения задач            | /api/internal/task-stream     |
| SSE_RECONNECT_INTERVAL    | Интервал переподключения SSE                | 5s                            |
| SSE_MAX_RECONNECT_ATTEMPTS| Максимум попыток переподключения SSE        | 10                            |
| SSE_HEARTBEAT_TIMEOUT     | Таймаут heartbeat SSE         | 60s                           |
| SSE_HEARTBEAT_INTERVAL    | Интервал heartbeat SSE        | 30s                           |
| SSE_MAX_DURATION          | Максимальная длительность SSE | 1h                            |

Все переменные можно задавать через окружение или конфиг-файл (см. internal/config/config.go).

## Быстрый старт
```bash
make dev
```

## Тесты и линтинг
```bash
make test
make lint
```

## Безопасность и рекомендации
- Не изменяйте логику heartbeat, poller и очередей без веской причины
- Не используйте сторонние библиотеки для concurrency
- Не храните секреты в публичных репозиториях

## Подробнее
- [internal/ollama/client.go](./internal/ollama/client.go) — взаимодействие с Ollama
- [internal/worker/sse_client.go](./internal/worker/sse_client.go) — SSE-клиенты
- [pkg/worker/pool.go](./pkg/worker/pool.go) — worker pool
- [internal/promptutils/prompt.go](./internal/promptutils/prompt.go) — prompt builder

## Лицензия
MIT