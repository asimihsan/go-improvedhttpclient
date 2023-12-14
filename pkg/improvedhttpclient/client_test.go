package improvedhttpclient

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
)

type mockClock struct {
	now time.Time
}

func (c *mockClock) Now() time.Time {
	return c.now
}

func (c *mockClock) Sleep(d time.Duration) {
	c.now = c.now.Add(d)
}

func TestDo(t *testing.T) {
	clock := &mockClock{now: time.Now()}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewHTTPClient(
		WithClock(clock),
		WithRateLimitRequestsPerSecond(1),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	ctx := context.Background()
	resp, err := client.Do(ctx, req)
	defer func() {
		if resp == nil || resp.Body == nil {
			return
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("failed to close response body: %v", err)
		}
	}()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status code: got %v, want %v", resp.StatusCode, http.StatusOK)
	}
}

type logCaptureWriter struct {
	buf bytes.Buffer
}

func (w *logCaptureWriter) Write(p []byte) (n int, err error) {
	return w.buf.Write(p)
}

func (w *logCaptureWriter) String() string {
	return w.buf.String()
}

func TestLoggingTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)

		// write "response body"
		if _, err := w.Write([]byte("response body")); err != nil {
			t.Errorf("failed to write response body: %v", err)
		}

		// flush the response writer
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer server.Close()

	// Create a new log capture writer
	writer := &logCaptureWriter{}

	// Create a new logger that writes to the log capture writer
	logger := zerolog.New(writer).With().Timestamp().Logger()

	// Create a new HTTP client with wire logging enabled
	client, err := NewHTTPClient(
		WithWireLogging(&logger),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Make a request
	req, err := http.NewRequest("GET", server.URL, strings.NewReader("request body"))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	ctx := context.Background()
	resp, err := client.Do(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if resp == nil || resp.Body == nil {
			return
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("failed to close response body: %v", err)
		}
	}()

	// Check the captured logs
	logs := writer.String()
	fmt.Println(logs)

	if !strings.Contains(logs, "Sending request") {
		t.Errorf("expected logs to contain 'Sending request', got: %s", logs)
	}
	if !strings.Contains(logs, "Received response") {
		t.Errorf("expected logs to contain 'Received response', got: %s", logs)
	}
	if !strings.Contains(logs, "request body") {
		t.Errorf("expected logs to contain 'request body', got: %s", logs)
	}
	if !strings.Contains(logs, "response body") {
		t.Errorf("expected logs to contain 'response body', got: %s", logs)
	}
}

type testExporter struct {
	spans         []trace.ReadOnlySpan
	spansExported chan struct{}
}

func (e *testExporter) ExportSpans(ctx context.Context, spans []trace.ReadOnlySpan) error {
	e.spans = append(e.spans, spans...)

	// Signal that spans have been exported.
	close(e.spansExported)

	return nil
}

func (e *testExporter) Shutdown(ctx context.Context) error {
	// Optionally, add any shutdown logic here if needed.
	return nil
}

func initTestTracer() (*testExporter, *trace.TracerProvider, error) {
	exporter := &testExporter{}
	tp := trace.NewTracerProvider(
		trace.WithSampler(trace.AlwaysSample()),
		trace.WithSpanProcessor(trace.NewSimpleSpanProcessor(exporter)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return exporter, tp, nil
}

func TestOtelWrapper(t *testing.T) {
	exporter, tp, err := initTestTracer()
	if err != nil {
		t.Fatalf("failed to initialize tracer: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	otelWrapper := func(base http.RoundTripper) http.RoundTripper {
		return otelhttp.NewTransport(base)
	}

	client, err := NewHTTPClient(
		WithTransportWrapper(otelWrapper),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	ctx := context.Background()
	resp, err := client.Do(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		if resp == nil || resp.Body == nil {
			return
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("failed to close response body: %v", err)
		}
	}()

	if err := tp.Shutdown(context.Background()); err != nil {
		t.Errorf("error shutting down tracer provider: %v", err)
	}

	// Wait for spans to be exported
	<-exporter.spansExported

	if len(exporter.spans) == 0 {
		t.Errorf("expected at least one span to be exported, got none")
	}

	// Check the span attributes
	span := exporter.spans[0]
	fmt.Println(spew.Sdump(span))
}
