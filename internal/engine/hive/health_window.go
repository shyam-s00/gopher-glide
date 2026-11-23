package hive

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// healthRingSize is how many one-second readings are kept, so a window can reach
// back just under a minute.
const healthRingSize = 60

var _ engine.WindowedHealth = (*Engine)(nil)

// healthReading is the cumulative counter state at one instant.
type healthReading struct {
	at        time.Time
	completed int64 // success + failure
	failed    int64
	latN      int64 // latency samples written so far
}

// healthRing is a small ring of readings taken once per second. Writers are the
// sampler goroutine; readers are heartbeats and the controller, so a mutex is fine.
type healthRing struct {
	mu  sync.Mutex
	buf [healthRingSize]healthReading
	n   int // total readings pushed
}

func (r *healthRing) reset() {
	r.mu.Lock()
	r.n = 0
	r.mu.Unlock()
}

func (r *healthRing) push(rd healthReading) {
	r.mu.Lock()
	r.buf[r.n%healthRingSize] = rd
	r.n++
	r.mu.Unlock()
}

// reference returns the newest reading at least d older than now, or the oldest
// kept one when the run is younger than d.
func (r *healthRing) reference(now time.Time, d time.Duration) (healthReading, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return healthReading{}, false
	}
	kept := r.n
	if kept > healthRingSize {
		kept = healthRingSize
	}
	oldest := r.buf[(r.n-kept)%healthRingSize]
	for i := r.n - 1; i >= r.n-kept; i-- {
		rd := r.buf[i%healthRingSize]
		if now.Sub(rd.at) >= d {
			return rd, true
		}
	}
	return oldest, true
}

// readHealth loads the counters that already exist; it adds nothing to writers.
func (e *Engine) readHealth(now time.Time) healthReading {
	failed := e.counters.loadFailureCount()
	rd := healthReading{at: now, failed: failed, completed: e.counters.loadSuccessCount() + failed}
	if lb := e.latBuf.Load(); lb != nil {
		rd.latN = lb.n.Load()
	}
	return rd
}

// sampleHealth records one reading. The sampler calls it once a second.
func (e *Engine) sampleHealth(now time.Time) {
	e.health.push(e.readHealth(now))
}

// startHealthSampler clears the ring, takes the run-start reading and samples
// every second until the returned stop function is called.
func (e *Engine) startHealthSampler() (stop func()) {
	e.health.reset()
	e.sampleHealth(time.Now())

	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case now := <-t.C:
				e.sampleHealth(now)
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// HealthSince reports error rate and p99 over roughly the last d (one-second
// granularity). ok is false before the first reading or if the run restarted.
func (e *Engine) HealthSince(d time.Duration) (engine.HealthWindow, bool) {
	return e.healthSince(time.Now(), d)
}

func (e *Engine) healthSince(now time.Time, d time.Duration) (engine.HealthWindow, bool) {
	then, ok := e.health.reference(now, d)
	if !ok {
		return engine.HealthWindow{}, false
	}
	cur := e.readHealth(now)
	if cur.latN < then.latN || cur.completed < then.completed {
		return engine.HealthWindow{}, false
	}

	w := engine.HealthWindow{Seconds: math.Round(cur.at.Sub(then.at).Seconds()*10) / 10}
	if done := cur.completed - then.completed; done > 0 {
		w.ErrorRate = float64(cur.failed-then.failed) / float64(done)
	}
	w.P99Ms, w.Samples, w.Capped = e.latencyP99Since(then.latN, cur.latN)
	return w, true
}

// latencyP99Since is the p99 over latency samples written between counts from
// and to (newest first), reading at most latencySampleSize of them.
func (e *Engine) latencyP99Since(from, to int64) (p99 float64, samples int, capped bool) {
	lb := e.latBuf.Load()
	if lb == nil || len(lb.buf) == 0 {
		return 0, 0, false
	}
	capSize := int64(len(lb.buf))
	n := to - from
	if n > capSize {
		n = capSize
		capped = true
	}
	if n > latencySampleSize {
		n = latencySampleSize
		capped = true
	}
	if n <= 0 {
		return 0, 0, false
	}
	data := make([]float64, n)
	for i := int64(0); i < n; i++ {
		data[i] = math.Float64frombits(lb.buf[(to-1-i)%capSize].Load())
	}
	sort.Float64s(data)
	return percentile(data, 99), int(n), capped
}
