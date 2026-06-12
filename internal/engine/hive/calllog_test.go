package hive

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestEngine() *Engine {
	return New()
}

// ── logCall: success path ─────────────────────────────────────────────────────

func TestLogCall_SuccessPath(t *testing.T) {
	e := newTestEngine()
	e.logCall(0, "GET", "http://example.com/ok", http.StatusOK, 10*time.Millisecond, nil)

	logs := e.GetRecentLogs(10)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}

	l := logs[0]
	if l.Method != "GET" {
		t.Errorf("Method: want GET, got %s", l.Method)
	}
	if l.Url != "http://example.com/ok" {
		t.Errorf("Url: unexpected %s", l.Url)
	}
	if l.StatusCode != http.StatusOK {
		t.Errorf("StatusCode: want 200, got %d", l.StatusCode)
	}
	if l.Duration != 10*time.Millisecond {
		t.Errorf("Duration: want 10ms, got %s", l.Duration)
	}
	if l.Error != "" {
		t.Errorf("Error: expected empty, got %q", l.Error)
	}
}

// ── logCall: success NOT routed to errorLogs ──────────────────────────────────

func TestLogCall_SuccessNotInErrorLog(t *testing.T) {
	e := newTestEngine()
	e.logCall(0, "GET", "http://example.com/ok", http.StatusOK, 5*time.Millisecond, nil)

	errs := e.GetRecentErrorLogs(10)
	if len(errs) != 0 {
		t.Errorf("expected 0 error logs for 200, got %d", len(errs))
	}
}

// ── logCall: transport error path ─────────────────────────────────────────────

func TestLogCall_TransportError(t *testing.T) {
	e := newTestEngine()
	e.logCall(0, "POST", "http://example.com/fail", 0, 1*time.Millisecond, errors.New("connection refused"))

	logs := e.GetRecentLogs(10)
	if len(logs) != 1 {
		t.Fatalf("expected 1 call log, got %d", len(logs))
	}
	if logs[0].Error != "connection refused" {
		t.Errorf("Error: want 'connection refused', got %q", logs[0].Error)
	}

	errs := e.GetRecentErrorLogs(10)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error log, got %d", len(errs))
	}
	if errs[0].Error != "connection refused" {
		t.Errorf("error log Error field: got %q", errs[0].Error)
	}
}

// ── logCall: 4xx routed to errorLogs ─────────────────────────────────────────

func TestLogCall_4xxRoutedToErrorLog(t *testing.T) {
	e := newTestEngine()
	e.logCall(0, "GET", "http://example.com/notfound", http.StatusNotFound, 8*time.Millisecond, nil)

	errs := e.GetRecentErrorLogs(10)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error log for 404, got %d", len(errs))
	}
	if errs[0].StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode: want 404, got %d", errs[0].StatusCode)
	}
}

// ── logCall: 5xx routed to errorLogs ─────────────────────────────────────────

func TestLogCall_5xxRoutedToErrorLog(t *testing.T) {
	e := newTestEngine()
	e.logCall(0, "PUT", "http://example.com/err", http.StatusInternalServerError, 20*time.Millisecond, nil)

	errs := e.GetRecentErrorLogs(10)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error log for 500, got %d", len(errs))
	}
	if errs[0].StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode: want 500, got %d", errs[0].StatusCode)
	}
}

// ── buffer eviction cap ───────────────────────────────────────────────────────

func TestLogCall_BufferEvictionCap(t *testing.T) {
	e := newTestEngine() // maxLogs = 100

	// Insert 120 entries — oldest 20 must be evicted.
	for i := 0; i < 120; i++ {
		e.logCall(0, "GET", "http://example.com", http.StatusOK, time.Millisecond, nil)
	}

	logs := e.GetRecentLogs(200) // ask for more than the cap
	if len(logs) != 100 {
		t.Errorf("expected buffer capped at 100, got %d", len(logs))
	}
}

func TestLogCall_ErrorBufferEvictionCap(t *testing.T) {
	e := newTestEngine()

	for i := 0; i < 120; i++ {
		e.logCall(0, "GET", "http://example.com", http.StatusBadGateway, time.Millisecond, nil)
	}

	errs := e.GetRecentErrorLogs(200)
	if len(errs) != 100 {
		t.Errorf("expected error buffer capped at 100, got %d", len(errs))
	}
}

// ── count clamping ────────────────────────────────────────────────────────────

func TestGetRecentLogs_CountClamping(t *testing.T) {
	e := newTestEngine()

	// Insert only 3 entries, request 10.
	for i := 0; i < 3; i++ {
		e.logCall(0, "GET", "http://example.com", http.StatusOK, time.Millisecond, nil)
	}

	logs := e.GetRecentLogs(10)
	if len(logs) != 3 {
		t.Errorf("expected 3 (clamped), got %d", len(logs))
	}
}

func TestGetRecentLogs_ZeroCount(t *testing.T) {
	e := newTestEngine()
	e.logCall(0, "GET", "http://example.com", http.StatusOK, time.Millisecond, nil)

	logs := e.GetRecentLogs(0)
	if logs == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 entries for count=0, got %d", len(logs))
	}
}

// ── ordering — most recent entries are at the tail ────────────────────────────

func TestGetRecentLogs_Ordering(t *testing.T) {
	e := newTestEngine()

	urls := []string{"http://a.com", "http://b.com", "http://c.com"}
	for _, u := range urls {
		e.logCall(0, "GET", u, http.StatusOK, time.Millisecond, nil)
	}

	logs := e.GetRecentLogs(2) // should return b and c
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	if logs[0].Url != "http://b.com" {
		t.Errorf("logs[0]: want http://b.com, got %s", logs[0].Url)
	}
	if logs[1].Url != "http://c.com" {
		t.Errorf("logs[1]: want http://c.com, got %s", logs[1].Url)
	}
}

// ── LogCallForTest shim ───────────────────────────────────────────────────────

func TestLogCallForTest_Shim(t *testing.T) {
	e := newTestEngine()
	e.LogCallForTest("DELETE", "http://example.com/res", http.StatusNoContent, 3*time.Millisecond, nil)

	logs := e.GetRecentLogs(10)
	if len(logs) != 1 {
		t.Fatalf("expected 1 entry via shim, got %d", len(logs))
	}
	if logs[0].Method != "DELETE" {
		t.Errorf("Method: want DELETE, got %s", logs[0].Method)
	}
}

// ── returned entries are value copies (mutations don't affect the buffer) ──────

func TestGetRecentLogs_ReturnsCopies(t *testing.T) {
	e := newTestEngine()
	e.logCall(0, "GET", "http://example.com", http.StatusOK, time.Millisecond, nil)

	logs := e.GetRecentLogs(1)
	logs[0].Url = "http://mutated.com" // mutate the returned copy

	// The buffer should be unaffected.
	logs2 := e.GetRecentLogs(1)
	if logs2[0].Url != "http://example.com" {
		t.Errorf("buffer was mutated through returned value; got %s", logs2[0].Url)
	}
}

// ── concurrent safety ─────────────────────────────────────────────────────────

func TestLogCall_ConcurrentWritesAndReads(t *testing.T) {
	e := newTestEngine()
	done := make(chan struct{})

	// 10 concurrent writers
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				e.logCall(0, "GET", "http://example.com", http.StatusOK, time.Millisecond, nil)
			}
			done <- struct{}{}
		}()
	}

	// 5 concurrent readers
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				_ = e.GetRecentLogs(10)
				_ = e.GetRecentErrorLogs(10)
			}
			done <- struct{}{}
		}()
	}

	for i := 0; i < 15; i++ {
		<-done
	}

	logs := e.GetRecentLogs(100)
	if len(logs) > 100 {
		t.Errorf("buffer exceeded maxLogs cap under concurrent load: len=%d", len(logs))
	}
}
