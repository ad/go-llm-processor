package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ad/llm-proxy/processor/internal/config"
	"github.com/ad/llm-proxy/processor/internal/health"
	"github.com/ad/llm-proxy/processor/internal/ollama"
	"github.com/ad/llm-proxy/processor/internal/poller"
	"github.com/ad/llm-proxy/processor/internal/selfupdate"
	"github.com/ad/llm-proxy/processor/internal/worker"
)

var version = "v0.0.1"

func main() {
	cfg := config.Load()

	if !cfg.SelfUpdateEnabled {
		log.Println("Self-update is disabled, skipping...")
	} else {
		selfupdate.StartAutoUpdate(version, "ad/go-llm-processor", 5*time.Minute)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutdown signal received")
		cancel()
	}()

	log.Println("LLM Proxy Processor starting...")

	// Initialize services
	ollamaClient := ollama.NewClient(cfg.OllamaURL)
	workerAPIClient := worker.NewClient(cfg.WorkerURL, cfg.InternalAPIKey)

	// Start health check server
	healthServer := &http.Server{
		Addr:    cfg.HealthAddr,
		Handler: health.NewHandler(),
	}

	var wg sync.WaitGroup

	// Start health server
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("Health server starting on %s\n", cfg.HealthAddr)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Health server error: %v\n", err)
		}
	}()

	// Start task poller based on SSE_ENABLED setting
	wg.Add(1)
	go func() {
		defer wg.Done()
		if cfg.SSE.Enabled {
			log.Println("Starting SSE task poller...")
			taskPoller := poller.NewSSEPoller(workerAPIClient, ollamaClient, cfg)
			taskPoller.Start(ctx)
		} else {
			log.Println("Starting HTTP polling mode...")
			taskPoller := poller.NewImproved(workerAPIClient, ollamaClient, cfg)
			taskPoller.Start(ctx)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("Shutting down...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Health server shutdown error: %v\n", err)
	}

	wg.Wait()
	log.Println("LLM Proxy Processor stopped")
}
