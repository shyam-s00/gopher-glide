package dryrun

import (
	"net/url"
	"strings"
	"testing"
)

func TestRedactJSON_KeepsOrderAndMasksSensitiveKeys(t *testing.T) {
	in := `{"z":1,"a":{"password":"p","Token":"t","keep":"k","arr":[1,{"secret":[1,2]}]},"b":null,"session_id":null}`
	got, ok := redactJSON([]byte(in))
	if !ok {
		t.Fatal("valid JSON rejected")
	}
	want := `{"z":1,"a":{"password":"[redacted]","Token":"[redacted]","keep":"k","arr":[1,{"secret":["[redacted]","[redacted]"]}]},"b":null,"session_id":null}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestRedactJSON_PreservesBigNumbers(t *testing.T) {
	got, _ := redactJSON([]byte(`{"id":12345678901234567890,"f":1.50}`))
	if string(got) != `{"id":12345678901234567890,"f":1.50}` {
		t.Fatalf("got %s", got)
	}
}

func TestRedactJSON_RejectsInvalid(t *testing.T) {
	for _, in := range []string{``, `{"a":`, `{"a":1} trailing`, `plain text`, `[1,2`} {
		if _, ok := redactJSON([]byte(in)); ok {
			t.Errorf("%q should not be treated as JSON", in)
		}
	}
}

func TestRedactText(t *testing.T) {
	got := redactText(`error: password=hunter2 and "api_key": "abc" ok=1 Authorization: Bearer xyz`)
	for _, leak := range []string{"hunter2", `"abc"`, "xyz"} {
		if strings.Contains(got, strings.Trim(leak, `"`)) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	if !strings.Contains(got, "ok=1") {
		t.Errorf("harmless text was altered: %s", got)
	}
}

func TestRedactURL(t *testing.T) {
	u, _ := url.Parse("https://bob:s3cret@api.example.com/p?token=abc&page=2&API_KEY=k")
	got := redactURL(u)
	for _, leak := range []string{"s3cret", "abc", "=k"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q survived: %s", leak, got)
		}
	}
	if !strings.Contains(got, "page=2") || !strings.Contains(got, "bob") {
		t.Errorf("non-secret parts lost: %s", got)
	}
}

func TestPreview_TruncatesOnRuneBoundary(t *testing.T) {
	body := []byte(strings.Repeat("é", 100))
	got, trunc := preview(body, "text/plain", 11)
	if !trunc || len(got) > 11 || !strings.HasPrefix(string(body), got) {
		t.Fatalf("got %q (%d bytes) truncated=%v", got, len(got), trunc)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("cut inside a rune")
		}
	}
}

func TestPreview_BinaryAndEmpty(t *testing.T) {
	if got, _ := preview(nil, "text/plain", 10); got != "" {
		t.Errorf("empty body preview = %q", got)
	}
	if got, _ := preview([]byte{0, 1, 2}, "", 10); got != "<3 bytes, unknown content-type>" {
		t.Errorf("binary preview = %q", got)
	}
	if got, _ := preview([]byte("%PDF"), "application/pdf", 10); got != "<4 bytes, application/pdf>" {
		t.Errorf("pdf preview = %q", got)
	}
}

func TestSensitiveName(t *testing.T) {
	for _, n := range []string{"token", "accessToken", "X-Api-Key", "password", "Set-Cookie", "client_secret", "sessionId", "jwt"} {
		if !sensitiveName(n) {
			t.Errorf("%q should be sensitive", n)
		}
	}
	for _, n := range []string{"id", "name", "author", "count", "userId", "page"} {
		if sensitiveName(n) {
			t.Errorf("%q should not be sensitive", n)
		}
	}
}
