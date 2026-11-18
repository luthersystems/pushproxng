package main

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	http.Handle("/metrics", promhttp.Handler())
	// A server with a header timeout (gosec G114): http.ListenAndServe has
	// none, so a slow client could hold a connection open forever.
	srv := &http.Server{
		Addr:              ":9100",
		ReadHeaderTimeout: 10 * time.Second,
	}
	_ = srv.ListenAndServe()
}
