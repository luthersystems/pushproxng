package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luthersystems/pushproxng/common"
)

// This file replaces the old in.sh smoke test, which needed Docker and a
// golang:alpine image: it runs a scrape through the proxy in process.

// newTestProxy serves the proxy's handler with the pulser and GC running,
// as run does.
func newTestProxy(t *testing.T) *httptest.Server {
	t.Helper()
	s := NewState()
	startBackground(s)
	srv := httptest.NewServer(newHandler(s))
	t.Cleanup(srv.Close)
	return srv
}

// clientOnce does one poll, forward and push, as client/client.go does
// in its loop, against the target base URL.
func clientOnce(ctx context.Context, proxyURL, fqdn, target string, launched int) error {
	poll, err := json.Marshal(&common.PollRequest{FQDN: fqdn, Launched: launched})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL+"/poll", bytes.NewReader(poll))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("poll: status %d", resp.StatusCode)
	}
	var reply common.PollReply
	if err := json.Unmarshal(b, &reply); err != nil {
		return err
	}
	raw, err := hex.DecodeString(reply.Request)
	if err != nil {
		return err
	}
	scrape, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return err
	}
	scrape.URL.Scheme = "http"
	scrape.URL.Host = target
	scrape.RequestURI = ""
	tresp, err := http.DefaultClient.Do(scrape.WithContext(ctx))
	if err != nil {
		return err
	}
	var out bytes.Buffer
	err = tresp.Write(&out)
	_ = tresp.Body.Close()
	if err != nil {
		return err
	}
	push, err := json.Marshal(&common.PushRequest{FQDN: fqdn, UUID: reply.UUID, Response: hex.EncodeToString(out.Bytes())})
	if err != nil {
		return err
	}
	presp, err := http.Post(proxyURL+"/push", "application/json", bytes.NewReader(push))
	if err != nil {
		return err
	}
	_ = presp.Body.Close()
	if presp.StatusCode != http.StatusOK {
		return fmt.Errorf("push: status %d", presp.StatusCode)
	}
	return nil
}

func TestScrapeThroughProxy(t *testing.T) {
	const page = "# TYPE up gauge\nup 1\n"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, page)
	}))
	defer target.Close()
	proxy := newTestProxy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- clientOnce(ctx, proxy.URL, "chaincode.example", strings.TrimPrefix(target.URL, "http://"), 1)
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
	if err := <-clientErr; err != nil {
		t.Fatal(err)
	}
}

func TestPollSuperseded(t *testing.T) {
	proxy := newTestProxy(t)
	poll := func(launched int, wait time.Duration) (int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		b, _ := json.Marshal(&common.PollRequest{FQDN: "f", Launched: launched})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/poll", bytes.NewReader(b))
		if err != nil {
			return 0, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	// A newer client registers its launch time. There is no scrape, so
	// its poll waits until the timeout ends it.
	if code, err := poll(2, 300*time.Millisecond); err == nil {
		t.Fatalf("newer poll returned %d, want it to wait", code)
	}
	// A poll from an older launch is then refused with 423, so only the
	// newest client answers scrapes.
	code, err := poll(1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusLocked {
		t.Fatalf("older poll status %d, want 423", code)
	}
}

func TestBodyLimits(t *testing.T) {
	proxy := newTestProxy(t)
	for _, tc := range []struct {
		path  string
		limit int
	}{
		{"/poll", maxPollBytes},
		{"/push", maxPushBytes},
	} {
		body := bytes.Repeat([]byte("a"), tc.limit+1)
		resp, err := http.Post(proxy.URL+tc.path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s with %d bytes: status %d, want 413", tc.path, len(body), resp.StatusCode)
		}
	}
}

func TestProbeAndUnknownPath(t *testing.T) {
	proxy := newTestProxy(t)
	resp, err := http.Get(proxy.URL + "/probe")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(b) != "+OK" {
		t.Fatalf("/probe: %d %q", resp.StatusCode, b)
	}
	resp, err = http.Get(proxy.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/nope: %d", resp.StatusCode)
	}
}
