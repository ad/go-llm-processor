package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
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

func Load() *Config {
	return &Config{
		OllamaURL:         getEnv("OLLAMA_URL", getEnv("OLLAMA_HOST", "http://ollama:11434")),
		WorkerURL:         getEnv("WORKER_URL", "http://wrangler:8080"),
		HealthAddr:        getEnv("HEALTH_ADDR", ":8081"),
		PollInterval:      getDuration("POLL_INTERVAL", 5*time.Second),
		MaxRetries:        getInt("MAX_RETRIES", 3),
		RequestTimeout:    getDuration("REQUEST_TIMEOUT", 30*time.Second),
		ModelName:         getEnv("MODEL_NAME", "gemma2:1b"),
		WorkerCount:       getInt("WORKER_COUNT", 3),
		QueueSize:         getInt("QUEUE_SIZE", 100),
		ProcessorID:       generateProcessorID(),
		HeartbeatInterval: getDuration("HEARTBEAT_INTERVAL", 15*time.Second),
		MaxBatchSize:      getInt("MAX_BATCH_SIZE", 5),
		InternalAPIKey:    getEnv("INTERNAL_API_KEY", "dev-internal-key"),
		SSE: SSEConfig{
			Enabled:              getBool("SSE_ENABLED", true),
			Endpoint:             getEnv("SSE_ENDPOINT", "/api/internal/task-stream"),
			ReconnectInterval:    getDuration("SSE_RECONNECT_INTERVAL", 5*time.Second),
			MaxReconnectAttempts: getInt("SSE_MAX_RECONNECT_ATTEMPTS", 10),
			HeartbeatTimeout:     getDuration("SSE_HEARTBEAT_TIMEOUT", 60*time.Second),
			HeartbeatInterval:    getDuration("SSE_HEARTBEAT_INTERVAL", 30*time.Second),
			MaxDuration:          getDuration("SSE_MAX_DURATION", time.Hour),
		},
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
