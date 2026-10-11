package hive

import (
	"sort"
	"sync"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// logShard holds one shard's call-log and error-log ring buffers behind its own
// mutex, so Actors on different shards never contend (mirrors the counter
// striping in metrics.go). Entries are written as values, no per-entry allocation.
type logShard struct {
	mu         sync.Mutex
	callLogs   []engine.CallLog
	callCount  int
	errorLogs  []engine.CallLog
	errorCount int
}

// logCall records a completed call into the shard's ring buffers: every call goes
// to callLogs, failures (transport error or 4xx/5xx) also to errorLogs. Writes
// overwrite the oldest slot; after the first call there is no allocation.
func (e *Engine) logCall(shard int, method, url string, statusCode int, duration time.Duration, err error) {
	entry := engine.CallLog{
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

	// Lazily (re)size on the rare occasion e.maxLogs differs from the
	// buffer's current length (e.g. it was changed since the last reset).
	if len(s.callLogs) != e.maxLogs {
		s.callLogs = make([]engine.CallLog, e.maxLogs)
		s.callCount = 0
	}
	s.callLogs[s.callCount%e.maxLogs] = entry
	s.callCount++

	if entry.Error != "" || entry.StatusCode >= 400 {
		if len(s.errorLogs) != e.maxLogs {
			s.errorLogs = make([]engine.CallLog, e.maxLogs)
			s.errorCount = 0
		}
		s.errorLogs[s.errorCount%e.maxLogs] = entry
		s.errorCount++
	}
}

// collectRecent merges one buffer across all shards, sorts by timestamp and
// returns the newest count entries. Each shard is locked only long enough to copy.
func (e *Engine) collectRecent(count int, errorLogs bool) []engine.CallLog {
	var all []engine.CallLog
	for i := range e.logShards {
		s := &e.logShards[i]
		s.mu.Lock()
		var src []engine.CallLog
		var n int
		if errorLogs {
			src, n = s.errorLogs, s.errorCount
		} else {
			src, n = s.callLogs, s.callCount
		}
		if n > len(src) {
			n = len(src)
		}
		all = append(all, src[:n]...)
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

	return all[len(all)-count:]
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
