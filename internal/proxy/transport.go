package proxy

import (
	"net"
	"net/http"
	"time"
)

// DefaultTransport returns a tuned http.Transport for LLM proxying:
//   - HTTP/2 enabled
//   - compression DISABLED (bytes are JSON/SSE; compression hurts streaming)
//   - generous idle pool so we reuse connections across requests
//   - no global timeout (LLM streams can be long; control by request context)
func DefaultTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       0, // unlimited
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
	}
}
