package proxy

import (
	"github.com/prometheus/client_golang/prometheus"
	"net/http"
)

// Middleware defines the signature for middleware functions
type Middleware func(http.Handler) http.Handler

// Metrics holds Prometheus metrics for the proxy
type Metrics struct {
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
	ErrorsTotal     *prometheus.CounterVec
}

// StatusRecorder is a custom ResponseWriter to capture the status code and implement Flusher
type StatusRecorder struct {
	http.ResponseWriter
	StatusCode int
}

// WriteHeader captures the status code for logging and metrics
func (rec *StatusRecorder) WriteHeader(code int) {
	rec.StatusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

// Flush Implement Flusher interface
func (rec *StatusRecorder) Flush() {
	if flusher, ok := rec.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// NewProxyMetrics initializes Prometheus metrics for the proxy
func NewProxyMetrics() *Metrics {
	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "proxy_requests_total",
				Help: "Total number of proxy requests",
			},
			[]string{"method", "endpoint"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "proxy_request_duration_seconds",
				Help:    "Duration of proxy requests in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "endpoint"},
		),
		ErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "proxy_errors_total",
				Help: "Total number of proxy errors",
			},
			[]string{"method", "endpoint", "error"},
		),
	}

	// Register metrics
	prometheus.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.ErrorsTotal,
	)

	return m
}

// OpenaiProxyMetrics holds Prometheus metrics for the OpenAI proxy
type OpenaiProxyMetrics struct {
	RequestsTotal           *prometheus.CounterVec
	RequestDuration         *prometheus.HistogramVec
	ErrorsTotal             *prometheus.CounterVec
	ChatCompletions         *prometheus.CounterVec
	ChatCompletionDurations *prometheus.HistogramVec
}

// NewOpenaiProxyMetrics initializes Prometheus metrics for the OpenAI proxy
func NewOpenaiProxyMetrics() *OpenaiProxyMetrics {
	m := &OpenaiProxyMetrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "openai_proxy_requests_total",
				Help: "Total number of OpenAI proxy requests",
			},
			[]string{"method", "path"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "openai_proxy_request_duration_seconds",
				Help:    "Duration of OpenAI proxy requests in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "path"},
		),
		ErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "openai_proxy_errors_total",
				Help: "Total number of OpenAI proxy errors",
			},
			[]string{"method", "path", "error"},
		),
		ChatCompletions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "openai_proxy_chat_completions_total",
				Help: "Total number of chat completion requests",
			},
			[]string{"model"},
		),
		ChatCompletionDurations: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "openai_proxy_chat_completion_duration_seconds",
				Help:    "Duration of chat completion requests in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"model"},
		),
	}

	// Register metrics
	prometheus.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.ErrorsTotal,
		m.ChatCompletions,
		m.ChatCompletionDurations,
	)

	return m
}
