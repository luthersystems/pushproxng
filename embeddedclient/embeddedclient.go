package embeddedclient

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"time"

	"bitbucket.org/luthersystems/pushproxng/common"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	mimeTypeJSON = "application/json"
)

func Work(proxy string, fqdn string) {
	log := (func(err error) {
		fmt.Printf("encountered: %s\n", err)
	})

	pause := (func() {
		time.Sleep(30 * time.Second)
	})

	launched := time.Now().Unix()

	prometheusHandler := promhttp.Handler()

	for {
		ag1 := &common.PollRequest{FQDN: fqdn, Launched: int(launched)}

		inp1, err := json.Marshal(ag1)
		if err != nil {
			log(err)
			break
		}

		respPoll, err := http.Post(fmt.Sprintf("http://%s/poll", proxy), mimeTypeJSON, bytes.NewReader(inp1))
		if err != nil {
			log(err)
			pause()
			continue
		}
		respPollBytes, err := ioutil.ReadAll(respPoll.Body)
		respPoll.Body.Close()
		if err != nil {
			log(err)
			pause()
			continue
		}

		var ag2 common.PollReply
		err = json.Unmarshal(respPollBytes, &ag2)
		if err != nil {
			log(err)
			pause()
			continue
		}

		requestBytes, err := hex.DecodeString(ag2.Request)
		if err != nil {
			log(err)
			pause()
			continue
		}

		request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(requestBytes)))
		if err != nil {
			log(err)
			pause()
			continue
		}

		request.URL.Scheme = "http"
		request.URL.Host = "localhost"
		request.RequestURI = ""

		recorder := httptest.NewRecorder()

		prometheusHandler.ServeHTTP(recorder, request)
		response := recorder.Result()

		var responseBytes bytes.Buffer
		err = response.Write(&responseBytes)
		if err != nil {
			log(err)
			pause()
			continue
		}

		ag3 := &common.PushRequest{
			FQDN:     fqdn,
			UUID:     ag2.UUID,
			Response: hex.EncodeToString(responseBytes.Bytes()),
		}

		inp3, err := json.Marshal(ag3)
		if err != nil {
			log(err)
			break
		}

		respPush, err := http.Post(fmt.Sprintf("http://%s/push", proxy), mimeTypeJSON, bytes.NewReader(inp3))
		if err != nil {
			log(err)
			pause()
			continue
		}
		respPush.Body.Close()
	}
}
