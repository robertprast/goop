package azure

import (
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// Health check function to update backend statuses periodically every 5s
func (e *AzureOpenAIEngine) startHealthCheck() {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for range ticker.C {
			for _, backend := range e.backends {
				backend.IsActive = isBackendAvailable(backend.BackendURL)
				if backend.IsActive {
					e.logger.Debugf("Backend %s is healthy", backend.BackendURL)
				} else {
					e.logger.Warnf("Backend %s is unhealthy", backend.BackendURL)
				}
			}
		}
	}()
}

func (e *AzureOpenAIEngine) selectLeastLoadedBackend() *BackendConfig {
	var selected *BackendConfig
	minConnections := int64(^uint64(0) >> 1) // Initialize with max possible value

	for _, backend := range e.backends {
		if backend.IsActive && atomic.LoadInt64(&backend.Connections) < minConnections {
			minConnections = atomic.LoadInt64(&backend.Connections)
			selected = backend
		}
	}

	if selected == nil {
		e.logger.Error("No active backends found")
		return e.backends[0] // fallback to the first backend
	}
	return selected
}

// https://learn.microsoft.com/en-us/azure/api-management/front-door-api-management?source=post_page-----4dba93c6467d--------------------------------#update-default-origin-group
func isBackendAvailable(backendURL *url.URL) bool {
	client := http.Client{
		Timeout: 2 * time.Second,
	}
	url := backendURL.String() + "/status-0123456789abcdef"
	resp, err := client.Get(url)
	if err != nil {
		logrus.Warnf("Failed to check backend status: %v", err)
		return false
	}
	return resp.StatusCode == http.StatusOK
}
