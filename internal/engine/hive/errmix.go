package hive

import (
	"sort"
	"sync"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

const (
	firstErrCode = 400
	lastErrCode  = 599
	topCodes     = 3
	topEndpoints = 3
)

var _ engine.ErrorMixer = (*Engine)(nil)

// RecentErrorMix counts the error-log entries newer than window by kind. It
// reads the existing rings in place under each shard's lock, once per call,
// and touches nothing on the request path.
func (e *Engine) RecentErrorMix(window time.Duration) (engine.ErrorMix, bool) {
	cutoff := time.Now().Add(-window)
	mix := engine.ErrorMix{WindowS: window.Seconds()}
	var codes [lastErrCode - firstErrCode + 1]int
	// Copied out so spec matching happens outside the shard lock.
	refsp := refPool.Get().(*[]errRef)
	refs := (*refsp)[:0]
	defer func() { *refsp = refs[:0]; refPool.Put(refsp) }()

	for i := range e.logShards {
		s := &e.logShards[i]
		s.mu.Lock()
		ring, written := s.errorLogs, s.errorCount
		n := written
		if n > len(ring) {
			n = len(ring)
		}
		for j := 0; j < n; j++ {
			en := &ring[j]
			if en.Timestamp.Before(cutoff) {
				continue
			}
			mix.Sampled++
			refs = append(refs, errRef{en.Method, en.Url, en.StatusCode})
			switch code := en.StatusCode; {
			case code == 0:
				mix.Transport++
			case code >= 500:
				mix.HTTP5xx++
				if code <= lastErrCode {
					codes[code-firstErrCode]++
				}
			case code >= 400:
				mix.HTTP4xx++
				codes[code-firstErrCode]++
			default:
				mix.Extract++
			}
		}
		// A full ring whose oldest entry is still in the window may have
		// overwritten older in-window errors.
		if len(ring) > 0 && written >= len(ring) && !ring[written%len(ring)].Timestamp.Before(cutoff) {
			mix.Capped = true
		}
		s.mu.Unlock()
	}

	if mix.Sampled == 0 {
		return engine.ErrorMix{}, false
	}
	mix.TopCodes = topStatusCodes(&codes)
	mix.TopEndpoints = topFailingEndpoints(e.epMatch.Load(), refs)
	return mix, true
}

var refPool = sync.Pool{New: func() any {
	r := make([]errRef, 0, numShards*100)
	return &r
}}

// errRef is the part of an error-log entry needed to attribute it to an endpoint.
type errRef struct {
	method, url string
	code        int
}

// endpointTally counts one endpoint's failures per status code (0 = transport).
type endpointTally struct {
	n     int
	codes []engine.CodeCount
}

func (t *endpointTally) add(code, n int) {
	t.n += n
	for i := range t.codes {
		if t.codes[i].Code == code {
			t.codes[i].N += n
			return
		}
	}
	t.codes = append(t.codes, engine.CodeCount{Code: code, N: n})
}

// dominant returns the most frequent status code (lowest wins a tie).
func (t *endpointTally) dominant() int {
	best := t.codes[0]
	for _, c := range t.codes[1:] {
		if c.N > best.N || (c.N == best.N && c.Code < best.Code) {
			best = c
		}
	}
	return best.Code
}

// topFailingEndpoints groups failures by spec template and returns the worst few.
// Identical (method, url, code) entries are folded first so each distinct call
// is matched to a template once.
func topFailingEndpoints(m *endpointMatcher, refs []errRef) []engine.EndpointError {
	if len(refs) == 0 {
		return nil
	}
	distinct := make(map[errRef]int, 64)
	for _, r := range refs {
		distinct[r]++
	}
	tallies := make(map[string]*endpointTally, 8)
	for r, n := range distinct {
		label := m.label(r.method, r.url)
		t := tallies[label]
		if t == nil {
			t = &endpointTally{}
			tallies[label] = t
		}
		t.add(r.code, n)
	}

	out := make([]engine.EndpointError, 0, len(tallies))
	for label, t := range tallies {
		code := t.dominant()
		ee := engine.EndpointError{Endpoint: label, N: t.n}
		switch {
		case code == 0:
			ee.Kind = "transport"
		case code >= 500:
			ee.Kind, ee.Code = "http_5xx", code
		case code >= 400:
			ee.Kind, ee.Code = "http_4xx", code
		default:
			ee.Kind = "extract"
		}
		out = append(out, ee)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].N != out[b].N {
			return out[a].N > out[b].N
		}
		return out[a].Endpoint < out[b].Endpoint
	})
	if len(out) > topEndpoints {
		out = out[:topEndpoints]
	}
	return out
}

func topStatusCodes(codes *[lastErrCode - firstErrCode + 1]int) []engine.CodeCount {
	var out []engine.CodeCount
	for i, n := range codes {
		if n > 0 {
			out = append(out, engine.CodeCount{Code: firstErrCode + i, N: n})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].N != out[b].N {
			return out[a].N > out[b].N
		}
		return out[a].Code < out[b].Code
	})
	if len(out) > topCodes {
		out = out[:topCodes]
	}
	return out
}
