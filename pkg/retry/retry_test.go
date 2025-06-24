package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_Success(t *testing.T) {
	config := DefaultConfig()
	config.MaxRetries = 2
	config.BaseDelay = 10 * time.Millisecond

	attempts := 0
	operation := func() error {
		attempts++
		if attempts == 1 {
			return errors.New("first attempt fails")
		}
		return nil // Success on second attempt
	}

	err := Do(context.Background(), config, operation)
	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	if attempts != 2 {
		t.Errorf("Expected 2 attempts, got %d", attempts)
	}
}

func TestRetry_MaxRetriesExceeded(t *testing.T) {
	config := DefaultConfig()
	config.MaxRetries = 2
	config.BaseDelay = 10 * time.Millisecond

	attempts := 0
	operation := func() error {
		attempts++
		return errors.New("always fails")
	}

	err := Do(context.Background(), config, operation)
	if err == nil {
		t.Error("Expected error after max retries")
	}

	expectedAttempts := config.MaxRetries + 1 // Initial attempt + retries
	if attempts != expectedAttempts {
		t.Errorf("Expected %d attempts, got %d", expectedAttempts, attempts)
	}
}

func TestRetry_ContextCancellation(t *testing.T) {
	config := DefaultConfig()
	config.MaxRetries = 5
	config.BaseDelay = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	attempts := 0
	operation := func() error {
		attempts++
		return errors.New("always fails")
	}

	err := Do(ctx, config, operation)
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context deadline exceeded, got: %v", err)
	}

	// Should have made at least one attempt but not all retries
	if attempts == 0 {
		t.Error("Expected at least one attempt")
	}
	if attempts > config.MaxRetries+1 {
		t.Errorf("Made too many attempts despite context cancellation: %d", attempts)
	}
}

func TestExponentialBackoff(t *testing.T) {
	baseDelay := 100 * time.Millisecond

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
	}

	for _, test := range tests {
		result := ExponentialBackoff(test.attempt, baseDelay)
		if result != test.expected {
			t.Errorf("For attempt %d, expected %v, got %v", test.attempt, test.expected, result)
		}
	}
}
