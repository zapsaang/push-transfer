package deliver

import (
	"net"
	"net/http"
	"time"
)

// maxResponseDrainBytes bounds HTTP response draining to 64KB so Keep-Alive
// connections can be reused without risking unbounded stream consumption.
const maxResponseDrainBytes = 64 << 10

// NewDefaultHTTPClient creates an HTTP client with a tuned transport connection pool.
func NewDefaultHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}
