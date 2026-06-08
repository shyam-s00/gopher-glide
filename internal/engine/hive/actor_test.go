package hive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil); err != nil {
		t.Fatalf("expected nil for 200, got %v", err)
	}
}

func TestExecuteActor_200_LatencyRecorded(t *testing.T) {
	srv := newTestServer(http.StatusOK, "hi")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil)
	if n := e.latBuf.Load().n.Load(); n != 1 {
		t.Fatalf("expected 1 latency, got %d", n)
	}
}

func TestExecuteActor_200_ShardedLatencyIncremented(t *testing.T) {
	srv := newTestServer(http.StatusOK, "hi")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 3, nil)
	if e.counters.loadTotalLatency() == 0 {
		t.Fatal("expected non-zero sharded latency")
	}
}

func TestExecuteActor_200_LoggedAsSuccess(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil)
	logs := e.GetRecentLogs(1)
	if len(logs) != 1 || logs[0].StatusCode != 200 || logs[0].Error != "" {
		t.Fatalf("unexpected call log: %+v", logs)
	}
}

func TestExecuteActor_500_ReturnsErrHttpError(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "oops")
	defer srv.Close()
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil); err != ErrHttpError {
		t.Fatalf("expected ErrHttpError for 500, got %v", err)
	}
}

func TestExecuteActor_404_ReturnsErrHttpError(t *testing.T) {
	srv := newTestServer(http.StatusNotFound, "")
	defer srv.Close()
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil); err != ErrHttpError {
		t.Fatalf("expected ErrHttpError for 404, got %v", err)
	}
}

func TestExecuteActor_500_RoutedToErrorLog(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "")
	defer srv.Close()
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil)
	if len(e.GetRecentErrorLogs(1)) == 0 {
		t.Fatal("expected error log for 500")
	}
}

func TestExecuteActor_TransportError_ReturnsError(t *testing.T) {
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, "http://127.0.0.1:1"), 0, nil); err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

func TestExecuteActor_TransportError_RoutedToErrorLog(t *testing.T) {
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, "http://127.0.0.1:1"), 0, nil)
	if len(e.GetRecentErrorLogs(1)) == 0 {
		t.Fatal("expected transport error in error log")
	}
}

func TestExecuteActor_TransportError_LatencyRecorded(t *testing.T) {
	e := New()
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, "http://127.0.0.1:1"), 0, nil)
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
	_ = New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil)
	if gotUA != userAgent {
		t.Fatalf("expected UA=%q, got %q", userAgent, gotUA)
	}
}

func TestExecuteActor_SnapRecorder_CalledOnSuccess(t *testing.T) {
	srv := newTestServer(http.StatusOK, `{"id":1}`)
	defer srv.Close()
	rec := &capturingRecorder{}
	e := New(WithRecorder(rec), WithSampleRate(1.0))
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil)
	if len(rec.entries) != 1 || rec.entries[0].StatusCode != 200 {
		t.Fatalf("unexpected snap entries: %+v", rec.entries)
	}
}

func TestExecuteActor_SnapRecorder_CalledOnHttpError(t *testing.T) {
	srv := newTestServer(http.StatusInternalServerError, "")
	defer srv.Close()
	rec := &capturingRecorder{}
	e := New(WithRecorder(rec))
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil)
	if len(rec.entries) == 0 || rec.entries[0].Error == nil {
		t.Fatal("expected snap entry with Error for 5xx")
	}
}

func TestExecuteActor_NoRecorder_NoPanic(t *testing.T) {
	srv := newTestServer(http.StatusOK, "hi")
	defer srv.Close()
	if err := New().executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecuteActor_BodySampled_AtRate100(t *testing.T) {
	body := `{"ok":true}`
	srv := newTestServer(http.StatusOK, body)
	defer srv.Close()
	rec := &capturingRecorder{}
	e := New(WithRecorder(rec), WithSampleRate(1.0))
	_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), 0, nil)
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
		_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), i%numShards, nil)
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
	_ = New().executeActor(context.Background(), spec, 0, nil)
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
	if err := New().executeActor(ctx, specFor(t, http.MethodGet, srv.URL), 0, nil); err == nil {
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
			_ = e.executeActor(context.Background(), spec, shard%numShards, nil)
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
		_ = e.executeActor(context.Background(), specFor(t, http.MethodGet, srv.URL), shard, nil)
	}
	for shard := 0; shard < numShards; shard++ {
		if e.counters.totalLatency[shard].value == 0 {
			t.Errorf("shard %d has zero latency", shard)
		}
	}
}

func TestExecuteActor_InvalidURL_ReturnsError(t *testing.T) {
	spec := httpreader.RequestSpec{Method: http.MethodGet, URL: "://bad"}
	if err := New().executeActor(context.Background(), spec, 0, nil); err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestExecuteActor_InvalidURL_LatencyRecorded(t *testing.T) {
	e := New()
	_ = e.executeActor(context.Background(), httpreader.RequestSpec{Method: http.MethodGet, URL: "://bad"}, 0, nil)
	if n := e.latBuf.Load().n.Load(); n != 1 {
		t.Fatalf("expected 1 latency entry, got %d", n)
	}
}

func TestSpawnManifest_Fields(t *testing.T) {
	m := SpawnManifest{Count: 100, Duration: 3 * time.Second}
	if m.Count != 100 || m.Duration != 3*time.Second {
		t.Fatalf("unexpected: %+v", m)
	}
}

func TestSpawnManifest_ZeroValue(t *testing.T) {
	var m SpawnManifest
	if m.Count != 0 || m.Duration != 0 {
		t.Fatalf("expected zero, got %+v", m)
	}
}

// ── executeActor: variable injection via ActorMemory ─────────────────────────

func TestExecuteActor_Memory_InjectsVarIntoURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodGet,
		URL:     srv.URL + "/users/{{user_id}}",
		Headers: make(http.Header),
	}
	mem := newActorMemory()
	mem.Set("user_id", "42")

	if err := New().executeActor(context.Background(), spec, 0, &mem); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/users/42" {
		t.Errorf("expected path /users/42, got %q", gotPath)
	}
}

func TestExecuteActor_Memory_InjectsVarIntoHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Headers: make(http.Header),
	}
	spec.Headers.Set("Authorization", "Bearer {{token}}")

	mem := newActorMemory()
	mem.Set("token", "secret-jwt")

	if err := New().executeActor(context.Background(), spec, 0, &mem); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer secret-jwt" {
		t.Errorf("expected Authorization=Bearer secret-jwt, got %q", gotAuth)
	}
}

func TestExecuteActor_Memory_InjectsVarIntoBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodPost,
		URL:     srv.URL,
		Headers: make(http.Header),
		Body:    `{"user": "{{username}}"}`,
	}
	mem := newActorMemory()
	mem.Set("username", "gopher")

	if err := New().executeActor(context.Background(), spec, 0, &mem); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody != `{"user": "gopher"}` {
		t.Errorf("unexpected body: %q", gotBody)
	}
}

// ── executeActor: @gg-export extraction and storage ──────────────────────────

func TestExecuteActor_Export_JSONPath_StoresInMemory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"data": {"token": "jwt-abc"}}`)
	}))
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodPost,
		URL:     srv.URL,
		Headers: make(http.Header),
		Exports: []httpreader.ExportDirective{
			{VarName: "auth_token", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.data.token"},
		},
	}
	mem := newActorMemory()

	if err := New().executeActor(context.Background(), spec, 0, &mem); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, ok := mem.Get("auth_token")
	if !ok || val != "jwt-abc" {
		t.Errorf("expected auth_token=jwt-abc, got %q (ok=%v)", val, ok)
	}
}

func TestExecuteActor_Export_Regex_StoresInMemory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "session_id=cafebabe; path=/")
	}))
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Headers: make(http.Header),
		Exports: []httpreader.ExportDirective{
			{VarName: "session", Engine: httpreader.ExportEngineRegex, Pattern: "session_id=([a-zA-Z0-9]+)"},
		},
	}
	mem := newActorMemory()

	if err := New().executeActor(context.Background(), spec, 0, &mem); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val, ok := mem.Get("session")
	if !ok || val != "cafebabe" {
		t.Errorf("expected session=cafebabe, got %q (ok=%v)", val, ok)
	}
}

func TestExecuteActor_Export_MultipleDirectives_AllStored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"token": "tok123", "userId": 99}`)
	}))
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodPost,
		URL:     srv.URL,
		Headers: make(http.Header),
		Exports: []httpreader.ExportDirective{
			{VarName: "token", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.token"},
			{VarName: "user_id", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.userId"},
		},
	}
	mem := newActorMemory()

	if err := New().executeActor(context.Background(), spec, 0, &mem); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok, _ := mem.Get("token"); tok != "tok123" {
		t.Errorf("expected token=tok123, got %q", tok)
	}
	if uid, _ := mem.Get("user_id"); uid != "99" {
		t.Errorf("expected user_id=99, got %q", uid)
	}
}

func TestExecuteActor_Export_BadPath_ReturnsExtractionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"other": "field"}`)
	}))
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Headers: make(http.Header),
		Exports: []httpreader.ExportDirective{
			{VarName: "token", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.missing.key"},
		},
	}
	mem := newActorMemory()

	err := New().executeActor(context.Background(), spec, 0, &mem)
	if err == nil {
		t.Fatal("expected error for bad export path, got nil")
	}
	if !strings.Contains(err.Error(), "extraction failed") {
		t.Errorf("expected ErrExtractionFailed in error chain, got %v", err)
	}
}

func TestExecuteActor_Export_On4xx_NotAttempted(t *testing.T) {
	// Export should NOT be attempted when the server returns 4xx.
	srv := newTestServer(http.StatusUnauthorized, `{"token":"should-not-extract"}`)
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Headers: make(http.Header),
		Exports: []httpreader.ExportDirective{
			{VarName: "token", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.token"},
		},
	}
	mem := newActorMemory()

	err := New().executeActor(context.Background(), spec, 0, &mem)
	if err != ErrHttpError {
		t.Fatalf("expected ErrHttpError for 401, got %v", err)
	}
	if mem.Len() != 0 {
		t.Errorf("expected empty memory after 4xx, got %d entries", mem.Len())
	}
}

func TestExecuteActor_Export_NilMemory_ExportsIgnored(t *testing.T) {
	// Passing nil memory should not panic even when the spec has exports.
	srv := newTestServer(http.StatusOK, `{"token":"abc"}`)
	defer srv.Close()

	spec := httpreader.RequestSpec{
		Method:  http.MethodGet,
		URL:     srv.URL,
		Headers: make(http.Header),
		Exports: []httpreader.ExportDirective{
			{VarName: "token", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.token"},
		},
	}
	if err := New().executeActor(context.Background(), spec, 0, nil); err != nil {
		t.Fatalf("unexpected error with nil memory: %v", err)
	}
}

// ── executeJourney: sequential execution ──────────────────────────────────────

func TestExecuteJourney_SingleSpec_WorksLikeExecuteActor(t *testing.T) {
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	specs := []httpreader.RequestSpec{{Method: http.MethodGet, URL: srv.URL, Headers: make(http.Header)}}
	if err := New().executeJourney(context.Background(), specs, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if served.Load() != 1 {
		t.Errorf("expected 1 request, got %d", served.Load())
	}
}

func TestExecuteJourney_MultipleSpecs_AllExecutedInOrder(t *testing.T) {
	var order []string
	var mu atomic.Int32 // use index as mutex-free ordering signal

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Add(1)
		order = append(order, "A")
		w.WriteHeader(http.StatusOK)
	}))
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Add(1)
		order = append(order, "B")
		w.WriteHeader(http.StatusOK)
	}))
	srvC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Add(1)
		order = append(order, "C")
		w.WriteHeader(http.StatusOK)
	}))
	defer srvA.Close()
	defer srvB.Close()
	defer srvC.Close()

	specs := []httpreader.RequestSpec{
		{Method: http.MethodGet, URL: srvA.URL, Headers: make(http.Header)},
		{Method: http.MethodGet, URL: srvB.URL, Headers: make(http.Header)},
		{Method: http.MethodGet, URL: srvC.URL, Headers: make(http.Header)},
	}
	if err := New().executeJourney(context.Background(), specs, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 || order[0] != "A" || order[1] != "B" || order[2] != "C" {
		t.Errorf("unexpected execution order: %v", order)
	}
}

func TestExecuteJourney_VariableFlowsAcrossSteps(t *testing.T) {
	// Step 1: returns JSON with a token. Step 2: must receive that token in its header.
	var step2Auth string

	srvLogin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"token": "bearer-xyz"}`)
	}))
	srvProfile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step2Auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srvLogin.Close()
	defer srvProfile.Close()

	step1 := httpreader.RequestSpec{
		Method:  http.MethodPost,
		URL:     srvLogin.URL,
		Headers: make(http.Header),
		Exports: []httpreader.ExportDirective{
			{VarName: "auth_token", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.token"},
		},
	}
	step2Headers := make(http.Header)
	step2Headers.Set("Authorization", "Bearer {{auth_token}}")
	step2 := httpreader.RequestSpec{
		Method:  http.MethodGet,
		URL:     srvProfile.URL,
		Headers: step2Headers,
	}

	if err := New().executeJourney(context.Background(), []httpreader.RequestSpec{step1, step2}, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step2Auth != "Bearer bearer-xyz" {
		t.Errorf("expected Authorization=Bearer bearer-xyz, got %q", step2Auth)
	}
}

func TestExecuteJourney_AbortsOnStepFailure(t *testing.T) {
	// Step 1 returns 500. Step 2 should NEVER be called.
	var step2Called atomic.Bool

	srv1 := newTestServer(http.StatusInternalServerError, "oops")
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		step2Called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()
	defer srv2.Close()

	specs := []httpreader.RequestSpec{
		{Method: http.MethodGet, URL: srv1.URL, Headers: make(http.Header)},
		{Method: http.MethodGet, URL: srv2.URL, Headers: make(http.Header)},
	}
	err := New().executeJourney(context.Background(), specs, 0)
	if err != ErrHttpError {
		t.Fatalf("expected ErrHttpError, got %v", err)
	}
	if step2Called.Load() {
		t.Error("step 2 was called after step 1 failed — journey should have aborted")
	}
}

func TestExecuteJourney_AbortsOnExtractionFailure(t *testing.T) {
	// Step 1 returns 200 but the export path is wrong. Step 2 must not run.
	var step2Called atomic.Bool

	srv1 := newTestServer(http.StatusOK, `{"other": "value"}`)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		step2Called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()
	defer srv2.Close()

	specs := []httpreader.RequestSpec{
		{
			Method:  http.MethodGet,
			URL:     srv1.URL,
			Headers: make(http.Header),
			Exports: []httpreader.ExportDirective{
				{VarName: "token", Engine: httpreader.ExportEngineJSONPath, Pattern: "$.missing"},
			},
		},
		{Method: http.MethodGet, URL: srv2.URL, Headers: make(http.Header)},
	}
	err := New().executeJourney(context.Background(), specs, 0)
	if err == nil {
		t.Fatal("expected extraction error, got nil")
	}
	if step2Called.Load() {
		t.Error("step 2 was called after extraction failure — journey should have aborted")
	}
}

func TestExecuteJourney_CountsEachStepIndividually(t *testing.T) {
	srv := newTestServer(http.StatusOK, "")
	defer srv.Close()

	e := New()
	specs := []httpreader.RequestSpec{
		{Method: http.MethodGet, URL: srv.URL, Headers: make(http.Header)},
		{Method: http.MethodGet, URL: srv.URL, Headers: make(http.Header)},
		{Method: http.MethodGet, URL: srv.URL, Headers: make(http.Header)},
	}
	if err := e.executeJourney(context.Background(), specs, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := e.counters.loadTotalRequests(); got != 3 {
		t.Errorf("expected totalRequests=3, got %d", got)
	}
	if got := e.counters.loadSuccessCount(); got != 3 {
		t.Errorf("expected successCount=3, got %d", got)
	}
}
