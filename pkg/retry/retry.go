package retry

import (
	"context"
	"fmt"
	"math"
	"time"
)

type Config struct {
	MaxRetries  int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	BackoffFunc func(attempt int, baseDelay time.Duration) time.Duration
}

func DefaultConfig() Config {
	return Config{
		MaxRetries:  3,
		BaseDelay:   1 * time.Second,
		MaxDelay:    30 * time.Second,
		BackoffFunc: ExponentialBackoff,
	}
}

func ExponentialBackoff(attempt int, baseDelay time.Duration) time.Duration {
	delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt)))
	return delay
}

func Do(ctx context.Context, config Config, operation func() error) error {
	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Calculate delay
			delay := config.BackoffFunc(attempt-1, config.BaseDelay)
			if delay > config.MaxDelay {
				delay = config.MaxDelay
			}

			// Wait with context cancellation support
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		if err := operation(); err != nil {
			lastErr = err
			if attempt == config.MaxRetries {
				return fmt.Errorf("operation failed after %d attempts: %w", config.MaxRetries+1, err)
			}
			continue
		}

		return nil
	}

	return lastErr
}
