package hive

import (
	"time"

	"github.com/shyam-s00/gopher-glide/internal/engine"
)

// logCall records a completed HTTP call into the ring buffers.
//
// Every call (success or failure) is appended to callLogs.
// Calls that resulted in a transport error or an HTTP 4xx/5xx are also
// appended to errorLogs.
// Both buffers are hard-capped at maxLogs entries; oldest entries are
// evicted when the cap is exceeded.
//
// logCall is safe to call concurrently — it holds callLogsMu for the
// duration of the append + eviction, then releases before returning.
func (e *Engine) logCall(method, url string, statusCode int, duration time.Duration, err error) {
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

	e.callLogsMu.Lock()
	defer e.callLogsMu.Unlock()

	e.callLogs = append(e.callLogs, entry)
	if len(e.callLogs) > e.maxLogs {
		e.callLogs = e.callLogs[len(e.callLogs)-e.maxLogs:]
	}

	if entry.Error != "" || entry.StatusCode >= 400 {
		e.errorLogs = append(e.errorLogs, entry)
		if len(e.errorLogs) > e.maxLogs {
			e.errorLogs = e.errorLogs[len(e.errorLogs)-e.maxLogs:]
		}
	}
}

// getRecentFromBuffer returns up to count entries from the tail of buffer,
// copying each pointer into a value slice so callers receive independent copies.
//
// Returns an empty (non-nil) slice when count is 0 or buffer is empty.
// Callers must hold at least callLogsMu.RLock() before calling.
func (e *Engine) getRecentFromBuffer(buffer []*engine.CallLog, count int) []engine.CallLog {
	if count > len(buffer) {
		count = len(buffer)
	}
	if count == 0 {
		return []engine.CallLog{}
	}
	src := buffer[len(buffer)-count:]
	logs := make([]engine.CallLog, count)
	for i, p := range src {
		logs[i] = *p
	}
	return logs
}

// LogCallForTest is an exported shim so tests in other packages (e.g. tui)
// can inject call-log entries without needing access to the unexported logCall.
func (e *Engine) LogCallForTest(method, url string, statusCode int, duration time.Duration, err error) {
	e.logCall(method, url, statusCode, duration, err)
}

// GetRecentLogs returns up to count of the most recent call log entries
// (both successes and failures) in chronological order.
func (e *Engine) GetRecentLogs(count int) []engine.CallLog {
	e.callLogsMu.RLock()
	defer e.callLogsMu.RUnlock()
	return e.getRecentFromBuffer(e.callLogs, count)
}

// GetRecentErrorLogs returns up to count of the most recent error log entries
// (HTTP 4xx/5xx or transport failures) in chronological order.
func (e *Engine) GetRecentErrorLogs(count int) []engine.CallLog {
	e.callLogsMu.RLock()
	defer e.callLogsMu.RUnlock()
	return e.getRecentFromBuffer(e.errorLogs, count)
}
