package utils

import (
	"net"
	"net/http"
	"time"
)

// HTTPClientConfig holds configuration for HTTP clients
type HTTPClientConfig struct {
	Timeout               time.Duration
	KeepAlive             time.Duration
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration
}

// DefaultHTTPClientConfig returns a default HTTP client configuration optimized for LLM APIs
func DefaultHTTPClientConfig() HTTPClientConfig {
	return HTTPClientConfig{
		Timeout:               120 * time.Second, // Long timeout for LLM streaming
		KeepAlive:             30 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxConnsPerHost:       20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// NewHTTPClient creates an optimized HTTP client with the given configuration
func NewHTTPClient(config HTTPClientConfig) *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: config.KeepAlive,
		}).DialContext,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
		MaxConnsPerHost:       config.MaxConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ExpectContinueTimeout: config.ExpectContinueTimeout,
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
	}
}

// DefaultHTTPClient returns a default optimized HTTP client
func DefaultHTTPClient() *http.Client {
	return NewHTTPClient(DefaultHTTPClientConfig())
}