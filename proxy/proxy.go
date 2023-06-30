package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/luthersystems/pushproxng/common"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type Scrape struct {
	started       time.Time
	uuid          string
	request       []byte
	requestTaken  bool
	response      []byte
	responseTaken bool
}

type FQDN struct {
	launched int
	scrapes  []*Scrape
}

type State struct {
	cond    *sync.Cond
	mutated int
	fqdn    map[string]*FQDN
}

func NewState() *State {
	return &State{
		cond:    sync.NewCond(&sync.Mutex{}),
		mutated: 0,
		fqdn:    make(map[string]*FQDN),
	}
}

// EnsureFQDN ensures that the given FQDN has been allocated an FQDN
// entry in the table, and returns the entry. This method should only
// be invoked under Interlock.
func (s *State) EnsureFQDN(fqdn string) *FQDN {
	if s.fqdn[fqdn] == nil {
		s.fqdn[fqdn] = &FQDN{
			launched: 0,
			scrapes:  make([]*Scrape, 0),
		}
		s.mutated++
	}
	return s.fqdn[fqdn]
}

// Interlock provides a mechanism for atomically observing and/or
// mutating the state repeatedly until a desired action has been
// completed. The "done" func is run in a critical section. It should
// return false if it is to wait for change signaling from other
// threads before executing again; otherwise, it should return true to
// indicate that the desired action has been completed. The "done"
// function should take care to increment the state's "mutated"
// counter if the state has been mutated during the course of its
// execution. Care should be taken to avoid livelock situations where,
// e.g., a pair of goroutines keep amending each other's mutations and
// waking each other.
//
// Additionally, the Interlock operation will be automatically
// cancelled if the given context is closed. The Interlock function
// returns an error if this occurred or nil otherwise.
func (s *State) Interlock(ctx context.Context, done func() bool) error {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
	for {
		unmutated := s.mutated
		b := done()
		if s.mutated != unmutated {
			s.cond.Broadcast()
		}
		if b {
			break
		} else if e := ctx.Err(); e != nil {
			return e
		} else {
			s.cond.Wait()
		}
	}
	return nil
}

func (s *State) InterlockMacroPostLaunched(ctx context.Context, fqdn string, launched int) error {
	return s.Interlock(ctx, (func() bool {
		entry := s.EnsureFQDN(fqdn)
		if launched > entry.launched {
			entry.launched = launched
			s.mutated++
		}
		return true
	}))
}

type InterlockMacroAwaitRequestValues struct {
	superseded bool
	uuid       string
	request    []byte
}

func (s *State) InterlockMacroAwaitRequest(ctx context.Context, fqdn string, launched int) (*InterlockMacroAwaitRequestValues, error) {
	var ret InterlockMacroAwaitRequestValues
	err := s.Interlock(ctx, (func() bool {
		entry := s.EnsureFQDN(fqdn)
		if launched < entry.launched {
			ret.superseded = true
			return true
		}
		for _, scrape := range entry.scrapes {
			if !scrape.requestTaken {
				ret.uuid = scrape.uuid
				ret.request = scrape.request
				scrape.requestTaken = true
				s.mutated++
				return true
			}
		}
		return false
	}))
	if err != nil {
		return nil, err
	}
	return &ret, nil
}

func (s *State) InterlockMacroPostPush(ctx context.Context, fqdn string, uuid string, response []byte) error {
	return s.Interlock(ctx, (func() bool {
		entry := s.EnsureFQDN(fqdn)
		for _, scrape := range entry.scrapes {
			if scrape.uuid == uuid {
				scrape.response = response
				s.mutated++
				return true
			}
		}
		return true
	}))
}

func (s *State) InterlockMacroPostRequest(ctx context.Context, fqdn string, started time.Time, id string, request []byte) error {
	return s.Interlock(ctx, (func() bool {
		entry := s.EnsureFQDN(fqdn)
		entry.scrapes = append(entry.scrapes, &Scrape{
			started:       started,
			uuid:          id,
			request:       request,
			requestTaken:  false,
			response:      nil,
			responseTaken: false,
		})
		s.mutated++
		return true
	}))
}

type InterlockMacroAwaitReplyValues struct {
	lost     bool
	response []byte
}

func (s *State) InterlockMacroAwaitReply(ctx context.Context, fqdn string, id string) (*InterlockMacroAwaitReplyValues, error) {
	var ret InterlockMacroAwaitReplyValues
	err := s.Interlock(ctx, (func() bool {
		entry := s.EnsureFQDN(fqdn)
		for _, scrape := range entry.scrapes {
			if scrape.uuid == id {
				if scrape.response != nil {
					ret.response = scrape.response
					scrape.responseTaken = true
					s.mutated++
					return true
				}
				return false
			}
		}
		ret.lost = true
		return true
	}))
	if err != nil {
		return nil, err
	}
	return &ret, nil
}

func run() {
	viper.SetEnvPrefix("pushproxng_proxy")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.BindEnv("listen")

	listenAddress := viper.GetString("listen")

	s := NewState()

	log := (func(err error) {
		fmt.Printf("encountered: %s\n", err)
	})

	// pulser thread
	go (func() {
		for {
			s.cond.L.Lock()
			s.cond.Broadcast()
			s.cond.L.Unlock()

			time.Sleep(time.Second)
		}
	})()

	// GC thread
	go (func() {
		fiveMinutes := 5 * time.Minute

		for {
			err := s.Interlock(context.Background(), (func() bool {
				for _, fqdn := range s.fqdn {
					keep := make([]*Scrape, 0)
					for _, scrape := range fqdn.scrapes {
						if time.Since(scrape.started) < fiveMinutes {
							keep = append(keep, scrape)
						} else {
							s.mutated++
						}
					}
					fqdn.scrapes = keep
				}
				return true
			}))
			if err != nil {
				log(err)
			}
			time.Sleep(fiveMinutes)
		}
	})()

	if err := http.ListenAndServe(listenAddress, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/poll" {
			jsonBytes, err := ioutil.ReadAll(r.Body)
			if err != nil {
				log(err)
				return
			}
			var ag1 common.PollRequest
			err = json.Unmarshal(jsonBytes, &ag1)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				log(err)
				return
			}
			err = s.InterlockMacroPostLaunched(r.Context(), ag1.FQDN, ag1.Launched)
			if err != nil {
				log(err)
				return
			}
			values, err := s.InterlockMacroAwaitRequest(r.Context(), ag1.FQDN, ag1.Launched)
			if err != nil {
				log(err)
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
				log(err)
				return
			}
			_, err = w.Write(out)
			if err != nil {
				log(err)
				return
			}
		} else if r.URL.Path == "/push" {
			jsonBytes, err := ioutil.ReadAll(r.Body)
			if err != nil {
				log(err)
				return
			}
			var ag1 common.PushRequest
			err = json.Unmarshal(jsonBytes, &ag1)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				log(err)
				return
			}
			response, err := hex.DecodeString(ag1.Response)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				log(err)
				return
			}
			err = s.InterlockMacroPostPush(r.Context(), ag1.FQDN, ag1.UUID, response)
			if err != nil {
				log(err)
				return
			}
			// success basically as long as the JSON parsed
			w.WriteHeader(http.StatusOK)
		} else if r.URL.Path == "/metrics" {
			fqdn := strings.Split(r.URL.Host, ":")[0]
			var requestBytes bytes.Buffer
			r.Header.Del("Accept-Encoding")
			err := r.Write(&requestBytes)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				log(err)
				return
			}
			id := uuid.New().String()
			err = s.InterlockMacroPostRequest(r.Context(), fqdn, time.Now(), id, requestBytes.Bytes())
			if err != nil {
				log(err)
				return
			}
			values, err := s.InterlockMacroAwaitReply(r.Context(), fqdn, id)
			if err != nil {
				log(err)
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
				log(err)
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			for _, key := range []string{"Content-Encoding", "Content-Type"} {
				w.Header()[key] = req.Header[key]
			}
			_, err = w.Write(body)
			if err != nil {
				log(err)
				return
			}
		} else if r.URL.Path == "/probe" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("+OK"))
		} else {
			fmt.Printf("http serve unknown: '%s'\n", r.URL.Path)
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		}
	})); err != nil {
		log(err)
	}
}

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Pushproxng's proxy command",
	Long:  "Pushproxng's proxy command",
	Run: func(cmd *cobra.Command, args []string) {
		run()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")

	rootCmd.Flags().String("listen", ":8080", "Listen address")
	viper.BindPFlag("listen", rootCmd.Flags().Lookup("listen"))
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	}

	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}

func main() {
	Execute()
}
