package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luthersystems/pushproxng/common"
	"github.com/luthersystems/pushproxng/internal/proxyserver"
)

// These tests replace the old in.sh smoke test, which needed Docker and a
// golang:alpine image: they run a scrape through the real proxy and the
// real client loop in one process.

func newProxy(t *testing.T) *httptest.Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := proxyserver.NewState()
	proxyserver.Start(ctx, s)
	srv := httptest.NewServer(proxyserver.Handler(s))
	t.Cleanup(srv.Close)
	return srv
}

func TestScrapeThroughProxy(t *testing.T) {
	const page = "# TYPE up gauge\nup 1\n"
	closed := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, page)
	}))
	defer target.Close()
	proxy := newProxy(t)
	proxyAddr := strings.TrimPrefix(proxy.URL, "http://")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	agentErr := make(chan error, 1)
	go func() {
		agentErr <- Once(ctx, proxyAddr, "chaincode.example", 1, func(r *http.Request) (*http.Response, error) {
			// As client/client.go does.
			r.URL.Scheme = "http"
			r.URL.Host = strings.TrimPrefix(target.URL, "http://")
			r.RequestURI = ""
			resp, err := http.DefaultClient.Do(r)
			if err == nil {
				resp.Body = &closeNotifier{ReadCloser: resp.Body, closed: closed}
			}
			return resp, err
		})
	}()

	// Prometheus scrapes through the proxy as an HTTP proxy, so the
	// request URL names the target's FQDN and port.
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	prom := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://chaincode.example:9100/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := prom.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape status %d, body %q", resp.StatusCode, body)
	}
	if string(body) != page {
		t.Fatalf("scrape body %q, want %q", body, page)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; version=0.0.4" {
		t.Fatalf("Content-Type %q", got)
	}
	if err := <-agentErr; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("agent did not close the target's response body")
	}
}

type closeNotifier struct {
	io.ReadCloser
	closed chan struct{}
}

func (c *closeNotifier) Close() error {
	select {
	case c.closed <- struct{}{}:
	default:
	}
	return c.ReadCloser.Close()
}

// TestRefusedPushIsAnError checks that a push the proxy refuses (413 for a
// response over its limit) is reported, not dropped in silence.
func TestRefusedPushIsAnError(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/poll":
			raw := "GET http://f/metrics HTTP/1.1\r\nHost: f\r\n\r\n"
			_ = json.NewEncoder(w).Encode(&common.PollReply{UUID: "id", Request: hex.EncodeToString([]byte(raw))})
		case "/push":
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		}
	}))
	defer fake.Close()
	err := Once(context.Background(), strings.TrimPrefix(fake.URL, "http://"), "f", 1, func(*http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		_, _ = io.WriteString(rec, "up 1\n")
		return rec.Result(), nil
	})
	if err == nil || !strings.Contains(err.Error(), "413") {
		t.Fatalf("Once = %v, want a 413 error", err)
	}
	if errors.Is(err, ErrFatal) {
		t.Fatalf("a refused push must be retried, got fatal %v", err)
	}
}
