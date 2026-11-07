package dryrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mustPlan(t *testing.T, content string) Plan {
	t.Helper()
	p, err := NewPlan(content)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, srv *httptest.Server, tmpl string, opts Options) Result {
	t.Helper()
	return Run(context.Background(), mustPlan(t, strings.ReplaceAll(tmpl, "BASE", srv.URL)), opts)
}

func TestRun_ChainPassesExtractedValueAlong(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"data":{"token":"abc123","user":{"id":42}}}`))
		case "/me/42":
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"name":"Ann"}`))
		}
	}))
	defer srv.Close()

	res := run(t, srv, `### login
POST BASE/login
# @gg-export token = jsonpath: $.data.token
# @gg-export uid = jsonpath: $.data.user.id

### me
GET BASE/me/{{uid}}
Authorization: Bearer {{token}}
`, Options{})

	if !res.OK || res.Passed != 2 || res.Failed != 0 || res.Skipped != 0 {
		t.Fatalf("result = %+v", res)
	}
	if gotAuth != "Bearer abc123" {
		t.Errorf("server saw Authorization %q", gotAuth)
	}
	login := res.Journeys[0].Steps[0]
	if login.HTTPStatus != 200 || login.Extracted["uid"] != "42" {
		t.Errorf("login step = %+v", login)
	}
	if !strings.HasPrefix(login.Extracted["token"], redacted) {
		t.Errorf("token must be masked, got %q", login.Extracted["token"])
	}
	if res.Journeys[0].Steps[1].BodyPreview != `{"name":"Ann"}` {
		t.Errorf("preview = %q", res.Journeys[0].Steps[1].BodyPreview)
	}
}

// A failing step ends its chain; later steps are reported as skipped and never sent.
func TestRun_FailingMiddleStepSkipsTheRest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/one":
			_, _ = w.Write([]byte(`{"id":"7"}`))
		case "/two/7":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad input"}`))
		}
	}))
	defer srv.Close()

	res := run(t, srv, `### one
GET BASE/one
# @gg-export id = jsonpath: $.id

### two
POST BASE/two/{{id}}
# @gg-export next = jsonpath: $.next

### three
GET BASE/three/{{next}}
`, Options{})

	steps := res.Journeys[0].Steps
	if res.OK || res.Passed != 1 || res.Failed != 1 || res.Skipped != 1 {
		t.Fatalf("counts = %+v", res)
	}
	if steps[0].Status != StatusPassed || steps[1].Status != StatusFailed || steps[2].Status != StatusSkipped {
		t.Fatalf("statuses = %s %s %s", steps[0].Status, steps[1].Status, steps[2].Status)
	}
	if steps[1].HTTPStatus != 400 || !strings.Contains(steps[1].Error, "HTTP 400") || !strings.Contains(steps[1].BodyPreview, "bad input") {
		t.Errorf("failed step should carry status and body: %+v", steps[1])
	}
	if steps[0].Extracted["id"] != "7" {
		t.Errorf("variables extracted before the failure must be returned: %+v", steps[0])
	}
	if hits.Load() != 2 {
		t.Errorf("server saw %d requests, the skipped step must not be sent", hits.Load())
	}
}

func TestRun_ExtractionFailureFailsStepAndShowsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"tok":"x"}}`))
	}))
	defer srv.Close()
	res := run(t, srv, "GET BASE/\n# @gg-export t = jsonpath: $.data.token\n", Options{})
	s := res.Journeys[0].Steps[0]
	if s.Status != StatusFailed || !strings.Contains(s.Error, "@gg-export t failed") || !strings.Contains(s.BodyPreview, `"tok"`) {
		t.Fatalf("step = %+v", s)
	}
}

func TestRun_NonJSONBodyForJSONPathAndRegexExtraction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><input name="csrf" value="Z9"></html>`))
	}))
	defer srv.Close()

	res := run(t, srv, "GET BASE/\n# @gg-export csrf = regex: value=\"(\\w+)\"\n", Options{})
	if !res.OK || res.Journeys[0].Steps[0].Extracted["csrf"] != "Z9" {
		t.Fatalf("regex over HTML: %+v", res)
	}
	res = run(t, srv, "GET BASE/\n# @gg-export x = jsonpath: $.a\n", Options{})
	s := res.Journeys[0].Steps[0]
	if s.Status != StatusFailed || !strings.Contains(s.Error, "invalid JSON") {
		t.Fatalf("jsonpath over HTML should fail clearly: %+v", s)
	}
}

func TestRun_TimeoutFailsStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	start := time.Now()
	res := run(t, srv, "GET BASE/slow\n", Options{StepTimeout: 100 * time.Millisecond})
	s := res.Journeys[0].Steps[0]
	if s.Status != StatusFailed || s.Error != "timed out" {
		t.Fatalf("step = %+v", s)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout not honoured, took %s", time.Since(start))
	}
}

func TestRun_ConnectionRefusedIsAStepError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	res := Run(context.Background(), mustPlan(t, "GET "+base+"/x\n"), Options{})
	s := res.Journeys[0].Steps[0]
	if s.Status != StatusFailed || s.Error == "" || s.HTTPStatus != 0 {
		t.Fatalf("step = %+v", s)
	}
}

func TestRun_UnresolvedVariableIsNeverSent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()

	tmpl := "GET BASE/items/{{id}}\n"
	res := run(t, srv, tmpl, Options{})
	if s := res.Journeys[0].Steps[0]; s.Status != StatusFailed || !strings.Contains(s.Error, "{{id}}") || hits.Load() != 0 {
		t.Fatalf("step=%+v hits=%d", s, hits.Load())
	}

	res = run(t, srv, tmpl, Options{Vars: map[string]string{"id": "9"}})
	if !res.OK || hits.Load() != 1 {
		t.Fatalf("supplied variable should run: %+v hits=%d", res, hits.Load())
	}
}

func TestRun_VarsDoNotLeakBetweenJourneys(t *testing.T) {
	var hitC atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/c") {
			hitC.Add(1)
		}
		_, _ = w.Write([]byte(`{"v":"1"}`))
	}))
	defer srv.Close()
	res := run(t, srv, `### a
GET BASE/a
# @gg-export v = jsonpath: $.v

### b
GET BASE/b/{{v}}

### mid
GET BASE/mid

### c
GET BASE/c/{{v}}
`, Options{})

	if len(res.Journeys) != 3 {
		t.Fatalf("journeys = %d, want 3", len(res.Journeys))
	}
	if !res.Journeys[0].OK || res.Journeys[1].Steps[0].Status != StatusPassed {
		t.Fatalf("first two journeys should pass: %+v", res.Journeys)
	}
	c := res.Journeys[2].Steps[0]
	if c.Status != StatusFailed || !strings.Contains(c.Error, "{{v}}") || hitC.Load() != 0 {
		t.Fatalf("v must not carry over to a later journey: %+v hits=%d", c, hitC.Load())
	}
}

func TestRun_CheckURLBlocksBeforeAnyTraffic(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	deny := func(u *url.URL) error { return errors.New("host not allowed: " + u.Host) }
	res := run(t, srv, "GET BASE/x\n", Options{CheckURL: deny})
	if s := res.Journeys[0].Steps[0]; s.Status != StatusFailed || !strings.Contains(s.Error, "not allowed") || hits.Load() != 0 {
		t.Fatalf("step=%+v hits=%d", s, hits.Load())
	}
}

func TestRun_RedirectToBlockedHostIsStopped(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret", http.StatusFound)
	}))
	defer redirector.Close()

	rhost := strings.TrimPrefix(redirector.URL, "http://")
	check := func(u *url.URL) error {
		if u.Host != rhost {
			return fmt.Errorf("host %s is not allowed", u.Host)
		}
		return nil
	}
	res := Run(context.Background(), mustPlan(t, "GET "+redirector.URL+"/go\n"), Options{CheckURL: check})
	s := res.Journeys[0].Steps[0]
	if s.Status != StatusFailed || !strings.Contains(s.Error, "not allowed") || targetHits.Load() != 0 {
		t.Fatalf("step=%+v targetHits=%d", s, targetHits.Load())
	}
}

func TestRun_HugeBodyFailsStepWithBoundedPreview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100_000)))
	}))
	defer srv.Close()
	res := run(t, srv, "GET BASE/\n", Options{MaxBodyBytes: 10_000})
	s := res.Journeys[0].Steps[0]
	if s.Status != StatusFailed || !strings.Contains(s.Error, "larger than") || len(s.BodyPreview) > DefaultPreviewBytes {
		t.Fatalf("step = status %s err %q preview %d", s.Status, s.Error, len(s.BodyPreview))
	}
}

// Output size must stay bounded however large the responses are.
func TestRun_SerializedResultIsSizeBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":"` + strings.Repeat("a", 500_000) + `"}`))
	}))
	defer srv.Close()
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "###\nGET BASE/%d\n\n", i)
	}
	res := run(t, srv, b.String(), Options{})
	raw, _ := json.Marshal(res)
	if len(raw) > 64<<10 {
		t.Fatalf("serialized result is %d bytes for 40 steps of 500 KB bodies", len(raw))
	}
	if res.Passed != 40 {
		t.Fatalf("passed = %d", res.Passed)
	}
	var truncated int
	for _, j := range res.Journeys {
		for _, s := range j.Steps {
			if s.Truncated {
				truncated++
			}
		}
	}
	if truncated != 40 {
		t.Errorf("truncated flag set on %d/40 steps", truncated)
	}
}

func TestRun_BinaryBodySummarised(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3, 0xff, 0xfe})
	}))
	defer srv.Close()
	s := run(t, srv, "GET BASE/\n", Options{}).Journeys[0].Steps[0]
	if s.BodyPreview != "<10 bytes, image/png>" || s.Truncated {
		t.Fatalf("preview = %q truncated=%v", s.BodyPreview, s.Truncated)
	}
}

// Nothing secret may appear anywhere in the serialized result.
func TestRun_SecretsNeverAppearInOutput(t *testing.T) {
	const (
		cookie  = "sess-COOKIE-9f8e7d"
		bearer  = "BEARER-TOKEN-1a2b3c"
		pass    = "hunter2-PASSWORD"
		apikey  = "AKIA-APIKEY-777"
		queryTk = "QUERY-SECRET-555"
		extract = "EXTRACTED-ACCESS-999"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: cookie})
		w.Header().Set("Authorization", "Bearer "+bearer)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"user":"ann","password":%q,"nested":{"api_key":%q,"list":["ok"],"auth_token":{"v":"%s"}},"access_token":%q,"count":3}`,
			pass, apikey, bearer, extract)
	}))
	defer srv.Close()

	res := run(t, srv, `GET BASE/?api_key=`+queryTk+`&page=2
Authorization: Bearer `+bearer+`
Cookie: sid=`+cookie+`
# @gg-export access_token = jsonpath: $.access_token
`, Options{})
	raw, _ := json.Marshal(res)
	out := string(raw)
	for _, secret := range []string{cookie, bearer, pass, apikey, queryTk, extract} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked in output:\n%s", secret, out)
		}
	}
	for _, keep := range []string{`ann`, `page=2`, `\"count\":3`, `ok`} {
		if !strings.Contains(out, keep) {
			t.Errorf("non-secret %q should survive redaction:\n%s", keep, out)
		}
	}
	if _, has := res.Journeys[0].Steps[0].Headers["Set-Cookie"]; has {
		t.Error("Set-Cookie must not be echoed")
	}
}

func TestRun_TransportErrorDoesNotLeakQuerySecret(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	res := Run(context.Background(), mustPlan(t, "GET "+base+"/x?token=LEAKME&a=1\n"), Options{})
	s := res.Journeys[0].Steps[0]
	if strings.Contains(s.Error+s.URL, "LEAKME") {
		t.Fatalf("query secret leaked: url=%q err=%q", s.URL, s.Error)
	}
}

func TestRun_CancelledContextStopsEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Run(ctx, mustPlan(t, strings.ReplaceAll("GET BASE/\n", "BASE", srv.URL)), Options{})
	if res.OK || res.Journeys[0].Steps[0].Status != StatusFailed {
		t.Fatalf("result = %+v", res)
	}
}
