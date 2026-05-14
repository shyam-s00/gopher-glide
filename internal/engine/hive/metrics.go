package hive

import (
	"sync"
	"sync/atomic"
	"time"
)

// metrics holds all atomic request counters.
// Each field is updated independently by Actor goroutines without coordination.
type metrics struct {
	totalRequests atomic.Int64
	successCount  atomic.Int64
	failureCount  atomic.Int64
	totalLatency  atomic.Int64 // cumulative milliseconds; used for avg latency
}

// ── rpsWindow ────────────────────────────────────────────────────────────────

// rpsWindowSize is the number of past seconds averaged to compute current RPS.
// 3 keeps the reading responsive to bursts without single-second oscillation.
const rpsWindowSize = 3

// rpsWindow is a fixed-size ring of per-second request counts used to compute
// a smooth, responsive current-RPS without cumulative lag.
//
// Each slot in the ring owns one calendar second (unix timestamp).
// When record() is called in a new second it resets the stale slot first,
// so old data never bleeds forward.
type rpsWindow struct {
	mu      sync.Mutex
	buckets [rpsWindowSize]int64 // request count recorded in that second
	seconds [rpsWindowSize]int64 // unix second the corresponding bucket belongs to
}

// record increments the count for the current second.
func (w *rpsWindow) record(count int64) {
	now := time.Now().Unix()
	w.mu.Lock()
	defer w.mu.Unlock()
	slot := int(now % rpsWindowSize)
	if w.seconds[slot] != now {
		// New second — reset the slot before accumulating.
		w.seconds[slot] = now
		w.buckets[slot] = 0
	}
	w.buckets[slot] += count
}

// rate returns the average request rate over the past (rpsWindowSize-1)
// fully-completed seconds.  The current (still-accumulating) second is
// excluded so the reading never oscillates at second boundaries.
func (w *rpsWindow) rate() float64 {
	now := time.Now().Unix()
	w.mu.Lock()
	defer w.mu.Unlock()

	var total int64
	for i := 0; i < rpsWindowSize; i++ {
		age := now - w.seconds[i]
		// Include only buckets that belong to a completed past second within
		// the window. age==0 is the current second (skip). age>=rpsWindowSize
		// is stale (skip).
		if age >= 1 && age < rpsWindowSize {
			total += w.buckets[i]
		}
	}

	// Always divide by the full window width to avoid oscillation.
	windowSecs := float64(rpsWindowSize - 1)
	if windowSecs < 1 {
		windowSecs = 1
	}
	return float64(total) / windowSecs
}
