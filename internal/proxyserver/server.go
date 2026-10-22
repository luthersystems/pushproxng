package proxyserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/luthersystems/pushproxng/common"
)

// Limits for the proxy's HTTP server. A poll is a long poll: the proxy
// holds it until a scrape request comes in or the client goes away, so the
// server has no write timeout and no whole-request read timeout. Instead
// the header timeout and a deadline on each body read stop a client that
// sends a request slowly from holding a connection and a goroutine open
// (Slowloris), and the body limits stop one large request from using all
// the memory of the proxy.
const (
	ReadHeaderTimeout = 10 * time.Second
	IdleTimeout       = 2 * time.Minute
	// MaxPollBytes bounds a /poll body: a small JSON object.
	MaxPollBytes = 1 << 20
	// MaxPushBytes bounds a /push body: a hex-encoded scrape response,
	// twice the size of the metrics page it carries.
	MaxPushBytes = 64 << 20
)

// bodyReadTimeout is how long a client has to send a /poll or /push body.
// A variable so that tests can shorten it.
var bodyReadTimeout = 30 * time.Second

// gcAge is how long the proxy keeps a scrape that no client has answered.
// When it is dropped, its /metrics caller gets 503.
const gcAge = 5 * time.Minute

// gcInterval is how often the GC runs. It is shorter than gcAge, so a
// scrape lives at most gcAge+gcInterval.
const gcInterval = time.Minute

// LogError logs err in the proxy's format.
func LogError(err error) {
	fmt.Printf("encountered: %s\n", err)
}

// NewServer returns the proxy's HTTP server, listening on addr, over the
// state s.
func NewServer(addr string, s *State) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Handler(s),
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
	}
}

// Start starts the goroutines that the state needs, until ctx ends: the
// pulser, which wakes every waiter once a second so that a waiter whose
// request context has ended can return, and the GC, which drops old
// scrapes.
func Start(ctx context.Context, s *State) {
	// pulser thread
	go (func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			s.cond.L.Lock()
			s.cond.Broadcast()
			s.cond.L.Unlock()
		}
	})()

	// GC thread
	go (func() {
		t := time.NewTicker(gcInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			err := s.Interlock(ctx, (func() bool {
				for _, fqdn := range s.fqdn {
					keep := make([]*Scrape, 0)
					for _, scrape := range fqdn.scrapes {
						if time.Since(scrape.started) < gcAge {
							keep = append(keep, scrape)
						} else {
							s.mutated++
						}
					}
					fqdn.scrapes = keep
				}
				return true
			}))
			if err != nil && ctx.Err() == nil {
				LogError(err)
			}
		}
	})()
}

// readBody reads at most limit bytes of the request body within
// bodyReadTimeout. It answers 413 and returns false when the body is
// larger, and 400 on another read error. After a whole body it clears the
// read deadline: once the body is read, net/http reads the connection in
// the background to see whether the client goes away, and a deadline that
// passed during the long poll would end the request.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	rc := http.NewResponseController(w)
	// An error means the writer cannot set deadlines (an httptest
	// recorder); the limit still applies.
	_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		// Keep the deadline: after the handler, net/http reads the
		// rest of the body to reuse the connection, and without a
		// deadline a slow client would hold it there.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		}
		LogError(err)
		return nil, false
	}
	_ = rc.SetReadDeadline(time.Time{})
	return b, true
}

// Handler returns the proxy's HTTP handler over the state s.
func Handler(s *State) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/poll":
			jsonBytes, ok := readBody(w, r, MaxPollBytes)
			if !ok {
				return
			}
			var ag1 common.PollRequest
			err := json.Unmarshal(jsonBytes, &ag1)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				LogError(err)
				return
			}
			err = s.InterlockMacroPostLaunched(r.Context(), ag1.FQDN, ag1.Launched)
			if err != nil {
				LogError(err)
				return
			}
			values, err := s.InterlockMacroAwaitRequest(r.Context(), ag1.FQDN, ag1.Launched)
			if err != nil {
				LogError(err)
				return
			}
			if values.superseded {
				http.Error(w, http.StatusText(http.StatusLocked), http.StatusLocked)
				return
			}
			// now it must be the case that value.{uuid,request} are sensibly set
			ag2 := &common.PollReply{
				UUID:    values.uuid,
				Request: hex.EncodeToString(values.request),
			}
			out, err := json.Marshal(ag2)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				LogError(err)
				return
			}
			_, err = w.Write(out)
			if err != nil {
				LogError(err)
				return
			}
		case "/push":
			jsonBytes, ok := readBody(w, r, MaxPushBytes)
			if !ok {
				return
			}
			var ag1 common.PushRequest
			err := json.Unmarshal(jsonBytes, &ag1)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				LogError(err)
				return
			}
			response, err := hex.DecodeString(ag1.Response)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				LogError(err)
				return
			}
			err = s.InterlockMacroPostPush(r.Context(), ag1.FQDN, ag1.UUID, response)
			if err != nil {
				LogError(err)
				return
			}
			// success basically as long as the JSON parsed
			w.WriteHeader(http.StatusOK)
		case "/metrics":
			// Prometheus sends the scrape as a proxy request, so the
			// URL holds the host of the target. Hostname drops the
			// port, also for a bracketed IPv6 address.
			fqdn := r.URL.Hostname()
			// A scrape is a GET with no body, but read any body
			// with the same limit and deadline as a poll, so a large
			// or slow body cannot use memory or hold the connection.
			reqBody, ok := readBody(w, r, MaxPollBytes)
			if !ok {
				return
			}
			// http.NoBody when empty: Request.Write sends a
			// non-nil empty body as chunked, which a GET to the
			// target must not get.
			r.Body = http.NoBody
			if len(reqBody) > 0 {
				r.Body = io.NopCloser(bytes.NewReader(reqBody))
			}
			var requestBytes bytes.Buffer
			r.Header.Del("Accept-Encoding")
			err := r.Write(&requestBytes)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				LogError(err)
				return
			}
			id := uuid.New().String()
			err = s.InterlockMacroPostRequest(r.Context(), fqdn, time.Now(), id, requestBytes.Bytes())
			if err != nil {
				LogError(err)
				return
			}
			values, err := s.InterlockMacroAwaitReply(r.Context(), fqdn, id)
			if err != nil {
				// The caller went away. Drop its scrape with a fresh
				// context: r.Context() has ended.
				if derr := s.InterlockMacroDropScrape(context.Background(), fqdn, id); derr != nil {
					LogError(derr)
				}
				LogError(err)
				return
			}
			if values.lost {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			// now it must be the case that response is sensibly set
			parts := bytes.SplitAfterN(values.response, []byte("\r\n\r\n"), 2)
			if len(parts) != 2 {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			headers := parts[0]
			body := parts[1]
			lines := bytes.SplitAfterN(headers, []byte("\r\n"), 2)
			if len(lines) != 2 {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET / HTTP/1.1\r\n" + string(lines[1]) + "\r\n\r\n")))
			if err != nil {
				LogError(err)
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			for _, key := range []string{"Content-Encoding", "Content-Type"} {
				w.Header()[key] = req.Header[key]
			}
			_, err = w.Write(body)
			if err != nil {
				LogError(err)
				return
			}
		case "/probe":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("+OK"))
		default:
			// %q, so a path with a newline cannot forge a log line.
			fmt.Printf("http serve unknown: %q\n", r.URL.Path)
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		}
	})
}
