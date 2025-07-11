package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"

	_ "github.com/joho/godotenv/autoload"
)

type Config struct {
	SelfUpdateEnabled bool
	OllamaURL         string
	WorkerURL         string
	HealthAddr        string
	PollInterval      time.Duration
	MaxRetries        int
	RequestTimeout    time.Duration
	ModelName         string
	WorkerCount       int
	QueueSize         int
	ProcessorID       string
	HeartbeatInterval time.Duration
	MaxBatchSize      int
	InternalAPIKey    string
	SSE               SSEConfig
	PollingEnabled    bool // новое поле
	InitialDelay      time.Duration
	DisableJitter     bool // для тестов: отключить jitter в poll interval
	WorkStealing      WorkStealingConfig
}

type SSEConfig struct {
	Enabled              bool          `env:"SSE_ENABLED" envDefault:"true"`
	Endpoint             string        `env:"SSE_ENDPOINT" envDefault:"/api/internal/task-stream"`
	ReconnectInterval    time.Duration `env:"SSE_RECONNECT_INTERVAL" envDefault:"5s"`
	MaxReconnectAttempts int           `env:"SSE_MAX_RECONNECT_ATTEMPTS" envDefault:"10"`
	HeartbeatTimeout     time.Duration `env:"SSE_HEARTBEAT_TIMEOUT" envDefault:"60s"`
	HeartbeatInterval    time.Duration `env:"SSE_HEARTBEAT_INTERVAL" envDefault:"30s"`
	MaxDuration          time.Duration `env:"SSE_MAX_DURATION" envDefault:"1h"`
}

type WorkStealingConfig struct {
	Enabled       bool          `env:"WORK_STEALING_ENABLED" envDefault:"true"`
	Interval      time.Duration `env:"WORK_STEALING_INTERVAL" envDefault:"120s"`
	MaxStealCount int           `env:"WORK_STEALING_MAX_COUNT" envDefault:"2"`
	MinCapacity   float64       `env:"WORK_STEALING_MIN_CAPACITY" envDefault:"0.3"` // минимальная свободная емкость для воровства
}

func Load() *Config {
	return &Config{
		SelfUpdateEnabled: getBool("SELF_UPDATE_ENABLED", true), // по умолчанию включено
		OllamaURL:         getEnv("OLLAMA_URL", getEnv("OLLAMA_HOST", "http://ollama:11434")),
		WorkerURL:         getEnv("WORKER_URL", "http://wrangler:8080"),
		HealthAddr:        getEnv("HEALTH_ADDR", ":8081"),
		PollInterval:      getDuration("POLL_INTERVAL", 60*time.Second),
		PollingEnabled:    getBool("POLLING_ENABLED", false), // по умолчанию включён
		MaxRetries:        getInt("MAX_RETRIES", 5),
		RequestTimeout:    getDuration("REQUEST_TIMEOUT", 30*time.Second),
		ModelName:         getEnv("MODEL_NAME", "gemma3:1b"),
		WorkerCount:       getInt("WORKER_COUNT", 2),
		QueueSize:         getInt("QUEUE_SIZE", 4),
		ProcessorID:       generateProcessorID(),
		HeartbeatInterval: getDuration("HEARTBEAT_INTERVAL", 60*time.Second),
		MaxBatchSize:      getInt("MAX_BATCH_SIZE", 5),
		InternalAPIKey:    getEnv("INTERNAL_API_KEY", "dev-internal-key"),
		SSE: SSEConfig{
			Enabled:              getBool("SSE_ENABLED", true),
			Endpoint:             getEnv("SSE_ENDPOINT", "/api/internal/task-stream"),
			ReconnectInterval:    getDuration("SSE_RECONNECT_INTERVAL", 5*time.Second),
			MaxReconnectAttempts: getInt("SSE_MAX_RECONNECT_ATTEMPTS", 10),
			HeartbeatTimeout:     getDuration("SSE_HEARTBEAT_TIMEOUT", 60*time.Second),
			HeartbeatInterval:    getDuration("SSE_HEARTBEAT_INTERVAL", 60*time.Second),
			MaxDuration:          getDuration("SSE_MAX_DURATION", time.Hour),
		},
		WorkStealing: WorkStealingConfig{
			Enabled:       getBool("WORK_STEALING_ENABLED", true),
			Interval:      getDuration("WORK_STEALING_INTERVAL", 120*time.Second),
			MaxStealCount: getInt("WORK_STEALING_MAX_COUNT", 2),
			MinCapacity:   getFloat64("WORK_STEALING_MIN_CAPACITY", 0.3),
		},
		InitialDelay:  getDuration("INITIAL_DELAY", 0),
		DisableJitter: getBool("DISABLE_JITTER", false), // по умолчанию включён
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

func getDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}

func getBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
}

func getFloat64(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
	}
	return defaultValue
}

func generateProcessorID() string {
	// Try to get from environment first (for Docker containers)
	if id := os.Getenv("PROCESSOR_ID"); id != "" {
		return id
	}

	// Generate random ID
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to hostname-based ID
		if hostname, err := os.Hostname(); err == nil {
			return fmt.Sprintf("proc-%s", hostname)
		}
		return "proc-unknown"
	}

	return fmt.Sprintf("proc-%s", hex.EncodeToString(bytes))
}
