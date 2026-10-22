// Package proxyserver is the pushprox proxy: Prometheus sends a scrape to
// it as an HTTP proxy request, a client inside the network of the target
// long-polls it for the scrape, runs it against the target and pushes the
// response back, and the proxy answers Prometheus with it. It is a package,
// not code in proxy/main, so that the client and the proxy can be tested
// together in one process (internal/agent).
package proxyserver

import (
	"context"
	"slices"
	"sync"
	"time"
)

type Scrape struct {
	started      time.Time
	uuid         string
	request      []byte
	requestTaken bool
	response     []byte
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
			started:      started,
			uuid:         id,
			request:      request,
			requestTaken: false,
			response:     nil,
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
		for i, scrape := range entry.scrapes {
			if scrape.uuid == id {
				if scrape.response != nil {
					ret.response = scrape.response
					// Drop the scrape as soon as its response is
					// delivered. Kept until the GC, every delivered
					// response (up to MaxPushBytes/2 each) would stay
					// in memory until it runs. slices.Delete also
					// clears the freed slot of the backing array.
					entry.scrapes = slices.Delete(entry.scrapes, i, i+1)
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

// InterlockMacroDropScrape removes the scrape id of fqdn, if it is still
// there. The /metrics handler calls it when its caller goes away (a
// Prometheus scrape timeout), so that no client runs the scrape and no
// response nobody reads is kept until the GC.
func (s *State) InterlockMacroDropScrape(ctx context.Context, fqdn string, id string) error {
	return s.Interlock(ctx, (func() bool {
		entry := s.EnsureFQDN(fqdn)
		for i, scrape := range entry.scrapes {
			if scrape.uuid == id {
				entry.scrapes = slices.Delete(entry.scrapes, i, i+1)
				s.mutated++
				break
			}
		}
		return true
	}))
}
