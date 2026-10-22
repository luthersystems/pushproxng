// Package agent is the client side of pushprox, which runs next to the
// target: it long-polls the proxy for a scrape, runs it, and pushes the
// response back. The client command (a target on another address) and
// embeddedclient (a handler in the same process) share it, so the loop is
// written and tested once.
package agent

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
	"time"

	"github.com/luthersystems/pushproxng/common"
)

const mimeTypeJSON = "application/json"

// Do runs one scrape request and returns its response.
type Do func(*http.Request) (*http.Response, error)

// ErrFatal wraps an error that a retry cannot fix, so Run stops.
var ErrFatal = errors.New("fatal")

// Once polls the proxy at proxyAddr (host:port) for one scrape of fqdn,
// runs it with do and pushes the response. launched identifies this
// client's start: the proxy refuses polls from an older start, so only the
// newest client answers. It returns an error that wraps ErrFatal when a
// retry cannot help.
func Once(ctx context.Context, proxyAddr, fqdn string, launched int, do Do) error {
	inp1, err := json.Marshal(&common.PollRequest{FQDN: fqdn, Launched: launched})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFatal, err)
	}
	pollBytes, err := post(ctx, fmt.Sprintf("http://%s/poll", proxyAddr), inp1)
	if err != nil {
		return fmt.Errorf("poll: %w", err)
	}

	var ag2 common.PollReply
	if err := json.Unmarshal(pollBytes, &ag2); err != nil {
		return err
	}
	requestBytes, err := hex.DecodeString(ag2.Request)
	if err != nil {
		return err
	}
	request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(requestBytes)))
	if err != nil {
		return err
	}
	request = request.WithContext(ctx)

	response, err := do(request)
	if err != nil {
		return err
	}
	var responseBytes bytes.Buffer
	err = response.Write(&responseBytes)
	// Write reads the body but does not close it; an open body keeps the
	// connection to the target from going back to the pool, so each
	// scrape would leak a connection.
	_ = response.Body.Close()
	if err != nil {
		return err
	}

	inp3, err := json.Marshal(&common.PushRequest{
		FQDN:     fqdn,
		UUID:     ag2.UUID,
		Response: hex.EncodeToString(responseBytes.Bytes()),
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFatal, err)
	}
	if _, err := post(ctx, fmt.Sprintf("http://%s/push", proxyAddr), inp3); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// post sends body to url and returns the response body. A status other
// than 200 is an error, so that a refused push (413 for a response over
// the proxy's limit) is logged, not lost.
func post(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mimeTypeJSON)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return b, nil
}

// Run calls Once in a loop. After an error it logs it and waits 30 seconds;
// it returns only on an error that wraps ErrFatal.
func Run(proxyAddr, fqdn string, do Do) error {
	launched := int(time.Now().Unix())
	for {
		err := Once(context.Background(), proxyAddr, fqdn, launched, do)
		if err == nil {
			continue
		}
		fmt.Printf("encountered: %s\n", err)
		if errors.Is(err, ErrFatal) {
			return err
		}
		time.Sleep(30 * time.Second)
	}
}
