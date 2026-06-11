package hive

import (
	"sort"
	"sync"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// logShard holds one shard's slice of the call-log and error-log ring
// buffers, each protected by its own mutex.
//
// Sharding mirrors the counter striping in metrics.go: Actor goroutines are
// assigned a shard index (actorIndex % numShards), so writes from different
// shards never contend on the same mutex. Without this, a single global lock
// taken on every completed request becomes a severe bottleneck at high
// concurrency (thousands of Actor goroutines serializing on one mutex).
type logShard struct {
	mu        sync.Mutex
	callLogs  []*engine.CallLog
	errorLogs []*engine.CallLog
}

// logCall records a completed HTTP call into the shard's ring buffers.
//
// Every call (success or failure) is appended to the shard's callLogs.
// Calls that resulted in a transport error or an HTTP 4xx/5xx are also
// appended to the shard's errorLogs.
// Both buffers are hard-capped at maxLogs entries per shard; oldest entries
// are evicted when the cap is exceeded.
//
// shard selects which logShard's mutex is taken — only Actors assigned to
// the same shard ever contend with each other.
func (e *Engine) logCall(shard int, method, url string, statusCode int, duration time.Duration, err error) {
	entry := &engine.CallLog{
		Timestamp:  time.Now(),
		Method:     method,
		Url:        url,
		StatusCode: statusCode,
		Duration:   duration,
	}
	if err != nil {
		entry.Error = err.Error()
	}

	s := &e.logShards[shard%numShards]
	s.mu.Lock()
	defer s.mu.Unlock()

	s.callLogs = append(s.callLogs, entry)
	if len(s.callLogs) > e.maxLogs {
		s.callLogs = s.callLogs[len(s.callLogs)-e.maxLogs:]
	}

	if entry.Error != "" || entry.StatusCode >= 400 {
		s.errorLogs = append(s.errorLogs, entry)
		if len(s.errorLogs) > e.maxLogs {
			s.errorLogs = s.errorLogs[len(s.errorLogs)-e.maxLogs:]
		}
	}
}

// collectRecent merges the requested buffer (callLogs or errorLogs) across
// all shards, sorts by timestamp, and returns the most recent `count`
// entries as value copies.
//
// Each shard is locked only long enough to copy its slice header's pointers
// — the per-shard critical section is O(maxLogs), not O(numShards*maxLogs).
func (e *Engine) collectRecent(count int, errorLogs bool) []engine.CallLog {
	var all []*engine.CallLog
	for i := range e.logShards {
		s := &e.logShards[i]
		s.mu.Lock()
		var src []*engine.CallLog
		if errorLogs {
			src = s.errorLogs
		} else {
			src = s.callLogs
		}
		all = append(all, src...)
		s.mu.Unlock()
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})

	if count > len(all) {
		count = len(all)
	}
	if count == 0 {
		return []engine.CallLog{}
	}

	tail := all[len(all)-count:]
	logs := make([]engine.CallLog, count)
	for i, p := range tail {
		logs[i] = *p
	}
	return logs
}

// LogCallForTest is an exported shim so tests in other packages (e.g. tui)
// can inject call-log entries without needing access to the unexported logCall.
func (e *Engine) LogCallForTest(method, url string, statusCode int, duration time.Duration, err error) {
	e.logCall(0, method, url, statusCode, duration, err)
}

// GetRecentLogs returns up to count of the most recent call log entries
// (both successes and failures) in chronological order.
func (e *Engine) GetRecentLogs(count int) []engine.CallLog {
	return e.collectRecent(count, false)
}

// GetRecentErrorLogs returns up to count of the most recent error log entries
// (HTTP 4xx/5xx or transport failures) in chronological order.
func (e *Engine) GetRecentErrorLogs(count int) []engine.CallLog {
	return e.collectRecent(count, true)
}
