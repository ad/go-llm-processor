package ollama

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func newTestClient(fn roundTripFunc) *Client {
	return &Client{
		baseURL:    "http://mockollama",
		httpClient: &http.Client{Transport: fn},
		modelCache: make(map[string]bool),
	}
}

func TestEnsureModelAvailable_ModelExists(t *testing.T) {
	mockTags := `{"models":[{"name":"llama2"}]}`
	client := newTestClient(func(req *http.Request) *http.Response {
		if req.URL.Path == "/api/tags" {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(mockTags)),
				Header:     make(http.Header),
			}
		}
		t.Fatalf("unexpected request: %s", req.URL.Path)
		return nil
	})

	err := client.EnsureModelAvailable("llama2")
	if err != nil {
		t.Fatalf("Ожидалась доступность модели, но ошибка: %v", err)
	}
}

func TestEnsureModelAvailable_ModelNeedsPull(t *testing.T) {
	step := 0
	client := newTestClient(func(req *http.Request) *http.Response {
		if req.URL.Path == "/api/tags" {
			if step == 0 {
				step++
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"models":[]}`)),
					Header:     make(http.Header),
				}
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"models":[{"name":"llama2"}]}`)),
				Header:     make(http.Header),
			}
		}
		if req.URL.Path == "/api/pull" {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"name":"llama2","status":"success"}`)),
				Header:     make(http.Header),
			}
		}
		t.Fatalf("unexpected request: %s", req.URL.Path)
		return nil
	})

	err := client.EnsureModelAvailable("llama2")
	if err != nil {
		t.Fatalf("Ожидалась успешная загрузка и доступность модели, но ошибка: %v", err)
	}
}

func TestEnsureModelAvailable_ModelNotFound(t *testing.T) {
	client := newTestClient(func(req *http.Request) *http.Response {
		if req.URL.Path == "/api/tags" {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"models":[]}`)),
				Header:     make(http.Header),
			}
		}
		if req.URL.Path == "/api/pull" {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"name":"llama2","status":"success"}`)),
				Header:     make(http.Header),
			}
		}
		t.Fatalf("unexpected request: %s", req.URL.Path)
		return nil
	})

	err := client.EnsureModelAvailable("llama2")
	if err == nil {
		t.Fatalf("Ожидалась ошибка, если модель не появилась даже после pull")
	}
}
