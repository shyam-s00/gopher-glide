package hive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
)

func specFor(t *testing.T, method, url string) httpreader.RequestSpec {
	t.Helper()
	return httpreader.RequestSpec{Method: method, URL: url}
}

func newTestServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
}

type capturingRecorder struct{ entries []snap.RecordEntry }

func (r *capturingRecorder) Record(e snap.RecordEntry)                       { r.entries = append(r.entries, e) }
func (r *capturingRecorder) Finalize(_ snap.RunMeta) (*snap.Snapshot, error) { return nil, nil }

func TestExecuteActor_200_ReturnsNil(t *testing.T) {
	srv := newTestServer(http.StatusOK, "OK")
	defer srv.Close()
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0); err != nil {
		t.Fatalf("expected nil for 200, got %v", err)
	}
}

func TestExecuteActor_200_LatencyRecorded(t *testing.T) {
	srv := newTestServer(http.StatusOK, "hi")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0)
	if n := e.latBuf.Load().n.Load(); n != 1 {
		t.Fatalf("expected 1 latency, got %d", n)
	}
}

func TestExecuteActor_200_ShardedLatencyIncremented(t *testing.T) {
	srv := newTestServer(http.StatusOK, "hi")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 3)
	if e.counters.loadTotalLatency() == 0 {
		t.Fatal("expected non-zero sharded latency")
	}
}

func TestExecuteActor_200_LoggedAsSuccess(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0)
	logs := e.GetRecentLogs(1)
	if len(logs) != 1 || logs[0].StatusCode != 200 || logs[0].Error != "" {
		t.Fatalf("unexpected call log: %+v", logs)
	}
}

func TestExecuteActor_500_ReturnsErrHttpError(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "oops")
	defer srv.Close()
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0); err != ErrHttpError {
		t.Fatalf("expected ErrHttpError for 500, got %v", err)
	}
}

func TestExecuteActor_404_ReturnsErrHttpError(t *testing.T) {
	srv := newTestServer(http.StatusNotFound, "")
	defer srv.Close()
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0); err != ErrHttpError {
		t.Fatalf("expected ErrHttpError for 404, got %v", err)
	}
}

func TestExecuteActor_500_RoutedToErrorLog(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0)
	if len(e.GetRecentErrorLogs(1)) == 0 {
		t.Fatal("expected error log for 500")
	}
}

func TestExecuteActor_TransportError_ReturnsError(t *testing.T) {
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, "http://127.0.0.1:1"), 0); err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

func TestExecuteActor_TransportError_RoutedToErrorLog(t *testing.T) {
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, "http://127.0.0.1:1"), 0)
	if len(e.GetRecentErrorLogs(1)) == 0 {
		t.Fatal("expected transport error in error log")
	}
}

func TestExecuteActor_TransportError_LatencyRecorded(t *testing.T) {
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, "http://127.0.0.1:1"), 0)
	if n := e.latBuf.Load().n.Load(); n != 1 {
		t.Fatalf("expected 1 latency entry, got %d", n)
	}
}

func TestExecuteActor_SetsUserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_ = New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0)
	if gotUA != userAgent {
		t.Fatalf("expected UA=%q, got %q", userAgent, gotUA)
	}
}

func TestExecuteActor_SnapRecorder_CalledOnSuccess(t *testing.T) {
	srv := newTestServer(http.StatusOK, `{"id":1}`)
	defer srv.Close()
	rec := &capturingRecorder{}
	e := New(WithRecorder(rec), WithSampleRate(1.0))
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0)
	if len(rec.entries) != 1 || rec.entries[0].StatusCode != 200 {
		t.Fatalf("unexpected snap entries: %+v", rec.entries)
	}
}

func TestExecuteActor_SnapRecorder_CalledOnHttpError(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "")
	defer srv.Close()
	rec := &capturingRecorder{}
	e := New(WithRecorder(rec))
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0)
	if len(rec.entries) == 0 || rec.entries[0].Error == nil {
		t.Fatal("expected snap entry with Error for 5xx")
	}
}

func TestExecuteActor_NoRecorder_NoPanic(t *testing.T) {
	srv := newTestServer(http.StatusOK, "hi")
	defer srv.Close()
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecuteActor_BodySampled_AtRate100(t *testing.T) {
	body := `{"ok":true}`
	srv := newTestServer(http.StatusOK, body)
	defer srv.Close()
	rec := &capturingRecorder{}
	e := New(WithRecorder(rec), WithSampleRate(1.0))
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0)
	if len(rec.entries) == 0 || string(rec.entries[0].RespBody) != body {
		t.Fatalf("expected body %q, got %q", body, rec.entries[0].RespBody)
	}
}

func TestExecuteActor_BodySampling_1In10Frequency(t *testing.T) {
	srv := newTestServer(http.StatusOK, `{"x":1}`)
	defer srv.Close()
	rec := &capturingRecorder{}
	e := New(WithRecorder(rec), WithSampleRate(0.10))
	const total = 100
	for i := 0; i < total; i++ {
		_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), i%numShards)
	}
	sampled := 0
	for _, entry := range rec.entries {
		if len(entry.RespBody) > 0 {
			sampled++
		}
	}
	if sampled != total/10 {
		t.Fatalf("expected %d sampled, got %d", total/10, sampled)
	}
}

func TestExecuteActor_POST_SendsRequestBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	spec := httpreader.RequestSpec{Method: http.MethodPost, URL: srv.URL, Body: `{"hello":"world"}`}
	_ = New().executeActor(context.Background(), spec, 0)
	if gotBody != `{"hello":"world"}` {
		t.Fatalf("expected body, got %q", gotBody)
	}
}

func TestExecuteActor_CancelledContext_ReturnsError(t *testing.T) {
	hangCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hangCh
		w.WriteHeader(http.StatusOK)
	}))
	defer func() { close(hangCh); srv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New().executeActor(ctx, specFor(t, http.MethodGet, srv.URL), 0); err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestExecuteActor_ConcurrentCalls_NoRace(t *testing.T) {
	srv := newTestServer(http.StatusOK, "ok")
	defer srv.Close()
	e := New()
	spec := specFor(t, http.MethodGet, srv.URL)
	var done atomic.Int32
	const goroutines = 20
	for i := 0; i < goroutines; i++ {
		go func(shard int) {
			_ = e.executeActor(context.Background(), spec, shard%numShards)
			done.Add(1)
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for done.Load() < goroutines {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExecuteActor_ShardRouting_EachShardReceivesLatency(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()
	e := New()
	for shard := 0; shard < numShards; shard++ {
		_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), shard)
	}
	for shard := 0; shard < numShards; shard++ {
		if e.counters.totalLatency[shard].value == 0 {
			t.Errorf("shard %d has zero latency", shard)
		}
	}
}

func TestExecuteActor_InvalidURL_ReturnsError(t *testing.T) {
	spec := httpreader.RequestSpec{Method: http.MethodGet, URL: "://bad"}
	if err := New().executeActor(context.Background(), spec, 0); err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestExecuteActor_InvalidURL_LatencyRecorded(t *testing.T) {
	e := New()
	_ = e.executeActor(context.Background(), httpreader.RequestSpec{Method: http.MethodGet, URL: "://bad"}, 0)
	if n := e.latBuf.Load().n.Load(); n != 1 {
		t.Fatalf("expected 1 latency entry, got %d", n)
	}
}

func TestSpawnManifest_Fields(t *testing.T) {
	m := SpawnManifest{Count: 100, SpecIndex: 3}
	if m.Count != 100 || m.SpecIndex != 3 {
		t.Fatalf("unexpected: %+v", m)
	}
}

func TestSpawnManifest_ZeroValue(t *testing.T) {
	var m SpawnManifest
	if m.Count != 0 || m.SpecIndex != 0 {
		t.Fatalf("expected zero, got %+v", m)
	}
}
