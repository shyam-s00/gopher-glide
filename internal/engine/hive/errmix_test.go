package hive

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

var errBoom = errors.New("boom")

// putError writes one error-log entry with an explicit timestamp, so tests
// control age without sleeping.
func putError(e *Engine, shard, status int, errMsg string, at time.Time) {
	s := &e.logShards[shard%numShards]
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorLogs[s.errorCount%len(s.errorLogs)] = engine.CallLog{Timestamp: at, StatusCode: status, Error: errMsg}
	s.errorCount++
}

func TestRecentErrorMix_ClassifiesByKind(t *testing.T) {
	e := New()
	// Real call path, so the classification matches what the engine records.
	e.logCall(0, "GET", "http://x/a", 0, time.Millisecond, errBoom)               // transport
	e.logCall(1, "GET", "http://x/b", 404, time.Millisecond, ErrHttpError)        // 4xx
	e.logCall(2, "GET", "http://x/c", 429, time.Millisecond, ErrHttpError)        // 4xx
	e.logCall(3, "GET", "http://x/d", 503, time.Millisecond, ErrHttpError)        // 5xx
	e.logCall(4, "GET", "http://x/e", 200, time.Millisecond, ErrExtractionFailed) // extract
	e.logCall(5, "GET", "http://x/f", 200, time.Millisecond, nil)                 // success: not an error

	mix, ok := e.RecentErrorMix(10 * time.Second)
	if !ok {
		t.Fatal("expected a mix")
	}
	if mix.Sampled != 5 || mix.Transport != 1 || mix.HTTP4xx != 2 || mix.HTTP5xx != 1 || mix.Extract != 1 {
		t.Errorf("mix = %+v", mix)
	}
	if mix.WindowS != 10 || mix.Capped {
		t.Errorf("window/capped = %v/%v", mix.WindowS, mix.Capped)
	}
}

// A 2xx whose @gg-export failed is an extraction failure, never a 4xx/5xx.
func TestRecentErrorMix_ExtractFailureIsItsOwnClass(t *testing.T) {
	e := New()
	for i := 0; i < 7; i++ {
		e.logCall(i, "POST", "http://x/login", 200, time.Millisecond, ErrExtractionFailed)
	}
	mix, _ := e.RecentErrorMix(time.Minute)
	if mix.Extract != 7 || mix.HTTP4xx != 0 || mix.HTTP5xx != 0 || mix.Transport != 0 || len(mix.TopCodes) != 0 {
		t.Errorf("mix = %+v", mix)
	}
}

func TestRecentErrorMix_NoErrorsReportsNotOK(t *testing.T) {
	e := New()
	if _, ok := e.RecentErrorMix(time.Minute); ok {
		t.Error("fresh engine must report no mix")
	}
	for i := 0; i < 50; i++ {
		e.logCall(i, "GET", "http://x", 200, time.Millisecond, nil)
	}
	if mix, ok := e.RecentErrorMix(time.Minute); ok {
		t.Errorf("successes must not produce a mix: %+v", mix)
	}
}

// Errors from before a quiet period must not count.
func TestRecentErrorMix_StaleEntriesIgnored(t *testing.T) {
	e := New()
	now := time.Now()
	for i := 0; i < 20; i++ {
		putError(e, i, 500, "old", now.Add(-time.Minute))
	}
	if _, ok := e.RecentErrorMix(10 * time.Second); ok {
		t.Fatal("entries a minute old are outside a 10 s window")
	}

	putError(e, 3, 502, "fresh", now.Add(-time.Second))
	putError(e, 4, 0, "dial", now.Add(-9*time.Second))
	putError(e, 5, 500, "edge", now.Add(-11*time.Second)) // just outside
	mix, ok := e.RecentErrorMix(10 * time.Second)
	if !ok || mix.Sampled != 2 || mix.HTTP5xx != 1 || mix.Transport != 1 {
		t.Fatalf("mix = %+v ok=%v", mix, ok)
	}
	if len(mix.TopCodes) != 1 || mix.TopCodes[0] != (engine.CodeCount{Code: 502, N: 1}) {
		t.Errorf("top codes = %+v", mix.TopCodes)
	}
}

func TestRecentErrorMix_WindowZeroSeesNothing(t *testing.T) {
	e := New()
	e.logCall(0, "GET", "http://x", 500, time.Millisecond, ErrHttpError)
	if _, ok := e.RecentErrorMix(0); ok {
		t.Error("a zero window should match nothing")
	}
}

// An error storm overwrites the ring; the mix covers what is retained and says so.
func TestRecentErrorMix_StormOverflowSetsCapped(t *testing.T) {
	e := New()
	perShard := e.maxLogs * 3
	for i := 0; i < perShard; i++ {
		e.logCall(0, "GET", "http://x", 503, time.Millisecond, ErrHttpError)
	}
	mix, ok := e.RecentErrorMix(time.Minute)
	if !ok {
		t.Fatal("expected a mix")
	}
	if mix.Sampled != e.maxLogs || mix.HTTP5xx != e.maxLogs {
		t.Errorf("retained entries = %d (5xx %d), want %d", mix.Sampled, mix.HTTP5xx, e.maxLogs)
	}
	if !mix.Capped {
		t.Error("an overflowed ring with fresh entries must set capped")
	}
}

// A full ring whose oldest entry already left the window lost nothing relevant.
func TestRecentErrorMix_FullRingWithStaleOldestIsNotCapped(t *testing.T) {
	e := New()
	now := time.Now()
	for i := 0; i < e.maxLogs; i++ {
		age := time.Minute
		if i >= e.maxLogs-10 {
			age = time.Second
		}
		putError(e, 0, 500, "x", now.Add(-age))
	}
	mix, _ := e.RecentErrorMix(10 * time.Second)
	if mix.Sampled != 10 || mix.Capped {
		t.Errorf("sampled=%d capped=%v, want 10/false", mix.Sampled, mix.Capped)
	}
}

func TestRecentErrorMix_NotCappedUntilRingIsFull(t *testing.T) {
	e := New()
	for i := 0; i < e.maxLogs-1; i++ {
		e.logCall(0, "GET", "http://x", 500, time.Millisecond, ErrHttpError)
	}
	if mix, _ := e.RecentErrorMix(time.Minute); mix.Capped {
		t.Error("ring not yet full")
	}
	e.logCall(0, "GET", "http://x", 500, time.Millisecond, ErrHttpError)
	if mix, _ := e.RecentErrorMix(time.Minute); !mix.Capped {
		t.Error("a full ring whose oldest entry is fresh may have dropped errors")
	}
}

func TestRecentErrorMix_TopCodesOrderAndLimit(t *testing.T) {
	e := New()
	add := func(code, n int) {
		for i := 0; i < n; i++ {
			e.logCall(i, "GET", "http://x", code, time.Millisecond, ErrHttpError)
		}
	}
	add(503, 5)
	add(429, 5) // ties with 503: lower code first
	add(500, 3)
	add(404, 1)
	mix, _ := e.RecentErrorMix(time.Minute)
	want := []engine.CodeCount{{Code: 429, N: 5}, {Code: 503, N: 5}, {Code: 500, N: 3}}
	if len(mix.TopCodes) != 3 {
		t.Fatalf("top codes = %+v", mix.TopCodes)
	}
	for i, w := range want {
		if mix.TopCodes[i] != w {
			t.Errorf("top[%d] = %+v, want %+v", i, mix.TopCodes[i], w)
		}
	}
	if mix.HTTP4xx != 6 || mix.HTTP5xx != 8 {
		t.Errorf("4xx=%d 5xx=%d", mix.HTTP4xx, mix.HTTP5xx)
	}
}

func TestRecentErrorMix_OddStatusCodesDoNotPanic(t *testing.T) {
	e := New()
	now := time.Now()
	putError(e, 0, 599, "x", now)
	putError(e, 0, 600, "x", now)
	putError(e, 0, 999, "x", now)
	putError(e, 0, 399, "x", now) // below 400 with an error: extract class
	mix, ok := e.RecentErrorMix(time.Minute)
	if !ok || mix.Sampled != 4 || mix.HTTP5xx != 3 || mix.Extract != 1 {
		t.Errorf("mix = %+v", mix)
	}
	if len(mix.TopCodes) != 1 || mix.TopCodes[0].Code != 599 {
		t.Errorf("only codes in 400-599 are listed: %+v", mix.TopCodes)
	}
}

func TestRecentErrorMix_ResetByNewRun(t *testing.T) {
	e := New()
	e.logCall(0, "GET", "http://x", 500, time.Millisecond, ErrHttpError)
	for i := range e.logShards {
		s := &e.logShards[i]
		s.mu.Lock()
		s.errorLogs = make([]engine.CallLog, e.maxLogs)
		s.errorCount = 0
		s.mu.Unlock()
	}
	if _, ok := e.RecentErrorMix(time.Minute); ok {
		t.Error("after a ring reset there are no errors")
	}
}

// Writers on every shard while the mix is read repeatedly; run with -race.
func TestRecentErrorMix_ConcurrentWriters(t *testing.T) {
	e := New()
	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				switch i % 3 {
				case 0:
					e.logCall(shard, "GET", "http://x", 500, time.Microsecond, ErrHttpError)
				case 1:
					e.logCall(shard, "GET", "http://x", 0, time.Microsecond, errBoom)
				default:
					e.logCall(shard, "GET", "http://x", 200, time.Microsecond, nil)
				}
			}
		}(w)
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	reads := 0
	for time.Now().Before(deadline) {
		if mix, ok := e.RecentErrorMix(time.Second); ok {
			if got := mix.Transport + mix.HTTP4xx + mix.HTTP5xx + mix.Extract; got != mix.Sampled {
				t.Fatalf("classes sum to %d, sampled %d", got, mix.Sampled)
			}
		}
		reads++
	}
	stop.Store(true)
	wg.Wait()
	if reads == 0 {
		t.Fatal("no reads happened")
	}
}

func TestEngineImplementsErrorMixer(t *testing.T) {
	var _ engine.ErrorMixer = New()
}
