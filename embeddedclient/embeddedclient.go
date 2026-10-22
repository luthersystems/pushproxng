// Package embeddedclient serves a process's own Prometheus metrics through
// a pushprox proxy, with no metrics listener: the scrape runs against the
// promhttp handler in process.
package embeddedclient

import (
	"net/http"
	"net/http/httptest"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/luthersystems/pushproxng/internal/agent"
)

// Work answers scrapes of fqdn that the proxy at proxy (host:port) holds,
// until an error a retry cannot fix. It logs other errors and retries
// after 30 seconds.
func Work(proxy string, fqdn string) {
	prometheusHandler := promhttp.Handler()
	_ = agent.Run(proxy, fqdn, func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme = "http"
		request.URL.Host = "localhost"
		request.RequestURI = ""
		recorder := httptest.NewRecorder()
		prometheusHandler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})
}
