package hive

import (
	"sort"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

const (
	firstErrCode = 400
	lastErrCode  = 599
	topCodes     = 3
)

var _ engine.ErrorMixer = (*Engine)(nil)

// RecentErrorMix counts the error-log entries newer than window by kind. It
// reads the existing rings in place under each shard's lock, once per call,
// and touches nothing on the request path.
func (e *Engine) RecentErrorMix(window time.Duration) (engine.ErrorMix, bool) {
	cutoff := time.Now().Add(-window)
	mix := engine.ErrorMix{WindowS: window.Seconds()}
	var codes [lastErrCode - firstErrCode + 1]int

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
	return mix, true
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
