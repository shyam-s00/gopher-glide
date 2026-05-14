package engine

import (
	"sync"
	"time"
)

// RpsWindowSize is the number of seconds averaged to compute current RPS.
// 3 keeps the reading responsive to bursts without single-second oscillation.
const RpsWindowSize = 3

// RpsWindow is a fixed-size ring of per-second request counts used to
// compute a smooth, responsive current-RPS without cumulative lag.
//
// Each slot owns one calendar second (unix timestamp). When Record() is called
// in a new second it resets the stale slot first, so old data never bleeds
// forward.
type RpsWindow struct {
	Mu      sync.Mutex
	Buckets [RpsWindowSize]int64 // request count for that second
	Seconds [RpsWindowSize]int64 // unix second the bucket belongs to
}

// Record increments the request count for the current second.
func (w *RpsWindow) Record(count int64) {
	now := time.Now().Unix()
	w.Mu.Lock()
	defer w.Mu.Unlock()
	slot := int(now % RpsWindowSize)
	if w.Seconds[slot] != now {
		w.Seconds[slot] = now
		w.Buckets[slot] = 0
	}
	w.Buckets[slot] += count
}

// Rate returns the average request rate over the past (RpsWindowSize-1)
// fully-completed seconds. The current (still-accumulating) second is
// excluded so the reading never oscillates at second boundaries.
func (w *RpsWindow) Rate() float64 {
	now := time.Now().Unix()
	w.Mu.Lock()
	defer w.Mu.Unlock()

	var total int64
	// Sum the fully-completed seconds before now.
	// Skipping "now" avoids a partial-second low reading at the boundary.
	for i := 0; i < RpsWindowSize; i++ {
		age := now - w.Seconds[i]
		if age >= 1 && age < RpsWindowSize {
			total += w.Buckets[i]
		}
	}
	// Divide by the number of complete seconds we looked at.
	windowSecs := float64(RpsWindowSize - 1)
	if windowSecs < 1 {
		windowSecs = 1
	}
	return float64(total) / windowSecs
}
