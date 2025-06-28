package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEClient_New(t *testing.T) {
	config := SSEConfig{
		Enabled:              true,
		Endpoint:             "/api/internal/task-stream",
		ReconnectInterval:    5 * time.Second,
		MaxReconnectAttempts: 10,
		HeartbeatTimeout:     60 * time.Second,
	}

	client := NewSSEClient("http://localhost:8080", "test-token", "test-processor", config)

	if client == nil {
		t.Fatal("Expected SSEClient to be created, got nil")
	}

	if client.baseURL != "http://localhost:8080" {
		t.Errorf("Expected baseURL 'http://localhost:8080', got '%s'", client.baseURL)
	}

	if client.token != "test-token" {
		t.Errorf("Expected token 'test-token', got '%s'", client.token)
	}

	if client.processorID != "test-processor" {
		t.Errorf("Expected processorID 'test-processor', got '%s'", client.processorID)
	}
}

func TestSSEClient_ParseEvent(t *testing.T) {
	client := NewSSEClient("http://test", "token", "processor", SSEConfig{})

	tests := []struct {
		name     string
		lines    []string
		expected SSEEvent
	}{
		{
			name: "heartbeat event",
			lines: []string{
				"event: heartbeat",
				"data: {\"message\":\"Connected\",\"timestamp\":1234567890}",
				"id: 123",
			},
			expected: SSEEvent{
				Type:      "heartbeat",
				ID:        "123",
				Timestamp: 1234567890,
			},
		},
		{
			name: "task available event",
			lines: []string{
				"event: task_available",
				"data: {\"taskId\":\"task-123\",\"priority\":1,\"retryCount\":0,\"timestamp\":1234567890}",
			},
			expected: SSEEvent{
				Type:      "task_available",
				Timestamp: 1234567890,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var event SSEEvent
			err := client.parseEvent(test.lines, &event)
			if err != nil {
				t.Fatalf("parseEvent failed: %v", err)
			}

			if event.Type != test.expected.Type {
				t.Errorf("Expected event type '%s', got '%s'", test.expected.Type, event.Type)
			}

			if event.ID != test.expected.ID {
				t.Errorf("Expected event ID '%s', got '%s'", test.expected.ID, event.ID)
			}

			if test.expected.Timestamp != 0 && event.Timestamp != test.expected.Timestamp {
				t.Errorf("Expected timestamp %d, got %d", test.expected.Timestamp, event.Timestamp)
			}
		})
	}
}

func TestSSEClient_HandleEvent(t *testing.T) {
	config := SSEConfig{
		Enabled: true,
	}
	client := NewSSEClient("http://test", "token", "processor", config)

	tests := []struct {
		name          string
		event         SSEEvent
		expectTask    bool
		expectedError bool
	}{
		{
			name: "task available event",
			event: SSEEvent{
				Type: "task_available",
				Data: json.RawMessage(`{"data":{"taskId":"task-123","priority":1,"retryCount":0}}`),
			},
			expectTask: true,
		},
		{
			name: "heartbeat event",
			event: SSEEvent{
				Type: "heartbeat",
				Data: json.RawMessage(`{"message":"heartbeat"}`),
			},
			expectTask: false,
		},
		{
			name: "error event",
			event: SSEEvent{
				Type: "error",
				Data: json.RawMessage(`{"error":"test error"}`),
			},
			expectedError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := client.handleEvent(test.event)

			if test.expectedError && err == nil {
				t.Error("Expected error, got nil")
			}

			if !test.expectedError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if test.expectTask {
				select {
				case task := <-client.GetTaskChannel():
					if task.TaskID != "task-123" {
						t.Errorf("Expected task ID 'task-123', got '%s'", task.TaskID)
					}
				case <-time.After(100 * time.Millisecond):
					t.Error("Expected task on channel, got none")
				}
			}
		})
	}
}

func TestSSEClient_MockServer(t *testing.T) {
	// Create mock SSE server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check authorization
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		// Send initial heartbeat
		w.Write([]byte("event: heartbeat\n"))
		w.Write([]byte("data: {\"message\":\"Connected\",\"timestamp\":1234567890}\n"))
		w.Write([]byte("\n"))
		flusher.Flush()

		// Send task available event
		w.Write([]byte("event: task_available\n"))
		w.Write([]byte("data: {\"data\":{\"taskId\":\"test-task\",\"priority\":1,\"retryCount\":0,\"timestamp\":1234567890}}\n"))
		w.Write([]byte("\n"))
		flusher.Flush()

		// Keep connection open briefly
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	config := SSEConfig{
		Enabled:              true,
		Endpoint:             "/api/internal/task-stream",
		ReconnectInterval:    1 * time.Second,
		MaxReconnectAttempts: 3,
		HeartbeatTimeout:     10 * time.Second,
	}

	// Replace server URL
	baseURL := strings.Replace(server.URL, "http://", "", 1)
	client := NewSSEClient("http://"+baseURL, "test-token", "test-processor", config)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start SSE client
	go client.Start(ctx)

	// Wait for task
	select {
	case task := <-client.GetTaskChannel():
		if task.TaskID != "test-task" {
			t.Errorf("Expected task ID 'test-task', got '%s'", task.TaskID)
		}
		if task.Priority != 1 {
			t.Errorf("Expected priority 1, got %d", task.Priority)
		}
	case <-time.After(3 * time.Second):
		t.Error("Expected task from SSE, got none")
	}

	client.Stop()
}

func TestSSEClient_Reconnection(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Fail first 2 attempts
			http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			return
		}

		// Success on 3rd attempt
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		w.Write([]byte("event: heartbeat\n"))
		w.Write([]byte("data: {\"message\":\"Connected after retry\",\"timestamp\":1234567890}\n"))
		w.Write([]byte("\n"))
		flusher.Flush()

		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	config := SSEConfig{
		Enabled:              true,
		Endpoint:             "/api/internal/task-stream",
		ReconnectInterval:    100 * time.Millisecond, // Fast reconnect for testing
		MaxReconnectAttempts: 5,
		HeartbeatTimeout:     10 * time.Second,
	}

	baseURL := strings.Replace(server.URL, "http://", "", 1)
	client := NewSSEClient("http://"+baseURL, "test-token", "test-processor", config)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go client.Start(ctx)

	// Should eventually succeed after retries
	time.Sleep(1 * time.Second)

	if attempts < 3 {
		t.Errorf("Expected at least 3 connection attempts, got %d", attempts)
	}

	client.Stop()
}
