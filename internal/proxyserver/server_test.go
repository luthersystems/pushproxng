package proxyserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luthersystems/pushproxng/common"
)

// newTestProxy serves the proxy with the pulser and GC running, as
// proxy/main does, and stops them when the test ends.
func newTestProxy(t *testing.T) (*httptest.Server, *State) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := NewState()
	Start(ctx, s)
	srv := httptest.NewUnstartedServer(Handler(s))
	srv.Config.ReadHeaderTimeout = ReadHeaderTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, s
}

func poll(t *testing.T, url string, launched int, wait time.Duration) (int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	b, _ := json.Marshal(&common.PollRequest{FQDN: "f", Launched: launched})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/poll", bytes.NewReader(b))
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

func TestPollSuperseded(t *testing.T) {
	proxy, _ := newTestProxy(t)
	// A newer client registers its launch time. There is no scrape, so
	// its poll waits until the timeout ends it.
	if code, err := poll(t, proxy.URL, 2, 300*time.Millisecond); err == nil {
		t.Fatalf("newer poll returned %d, want it to wait", code)
	}
	// A poll from an older launch is then refused with 423, so only the
	// newest client answers scrapes.
	code, err := poll(t, proxy.URL, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusLocked {
		t.Fatalf("older poll status %d, want 423", code)
	}
}

func TestBodyLimits(t *testing.T) {
	proxy, _ := newTestProxy(t)
	for _, tc := range []struct {
		path  string
		limit int
	}{
		{"/poll", MaxPollBytes},
		{"/push", MaxPushBytes},
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

// TestSlowBody checks that a client that sends its body slowly is cut off
// after bodyReadTimeout, and that the deadline does not end a long poll
// that waits longer than it once its body is read.
func TestSlowBody(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })
	proxy, _ := newTestProxy(t)

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "POST /push HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n{")
	if err != nil {
		t.Fatal(err)
	}
	// Send nothing more. The proxy must give up on the body and answer
	// (400) or close; either way the read below ends well before the
	// 5-second guard.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, _ = io.ReadAll(conn)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("slow body held the connection for %s", d)
	}

	// A poll with a whole body waits past bodyReadTimeout and ends only
	// by its own timeout, not by a read deadline left on the connection.
	start = time.Now()
	if code, err := poll(t, proxy.URL, 1, 1500*time.Millisecond); err == nil {
		t.Fatalf("long poll returned %d after %s, want it to wait", code, time.Since(start))
	}
	if d := time.Since(start); d < time.Second {
		t.Fatalf("long poll ended after %s, before its own timeout", d)
	}
}

// TestDeliveredScrapeFreed checks that a scrape leaves the state once its
// response is delivered, so responses do not pile up until the GC.
func TestDeliveredScrapeFreed(t *testing.T) {
	s := NewState()
	ctx := context.Background()
	if err := s.InterlockMacroPostRequest(ctx, "f", time.Now(), "id", []byte("req")); err != nil {
		t.Fatal(err)
	}
	if err := s.InterlockMacroPostPush(ctx, "f", "id", []byte("resp")); err != nil {
		t.Fatal(err)
	}
	v, err := s.InterlockMacroAwaitReply(ctx, "f", "id")
	if err != nil {
		t.Fatal(err)
	}
	if v.lost || string(v.response) != "resp" {
		t.Fatalf("reply %+v", v)
	}
	if n := len(s.fqdn["f"].scrapes); n != 0 {
		t.Fatalf("%d scrapes left after delivery, want 0", n)
	}
}

func TestProbeAndUnknownPath(t *testing.T) {
	proxy, _ := newTestProxy(t)
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
