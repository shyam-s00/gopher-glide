package dryrun

import (
	"fmt"
	"strings"
	"testing"
)

func hasIssue(issues []Issue, line int, sub string) bool {
	for _, i := range issues {
		if (line == 0 || i.Line == line) && strings.Contains(i.Message, sub) {
			return true
		}
	}
	return false
}

func TestValidate_Table(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantOK   bool
		errLine  int
		errSub   string
		warnLine int
		warnSub  string
	}{
		{name: "simple valid", content: "GET https://api.example.com/x\n", wantOK: true},
		{name: "valid chain", wantOK: true, content: `### login
POST https://a.example.com/login
Content-Type: application/json

{"u":"x"}

# @gg-export token = jsonpath: $.data.token

### me
GET https://a.example.com/me
Authorization: Bearer {{token}}
`},
		{name: "empty file", content: "", errSub: "no requests"},
		{name: "only comments", content: "# nothing\n// here\n", errSub: "no requests"},
		{name: "relative url", content: "GET /users\n", errLine: 1, errSub: "absolute"},
		{name: "relative url with variable", content: "GET /users/{{id}}\n", errLine: 1, errSub: "absolute"},
		{name: "scheme without host", content: "GET https:///x\n", errLine: 1, errSub: "absolute"},
		{name: "variable base url is accepted", wantOK: true, content: "GET https://x.example.com/cfg\n# @gg-export base = regex: (.*)\n\n###\nGET {{base}}/x\n"},
		{name: "self export and use", errLine: 1, errSub: "cannot use a variable it exports itself", content: "GET https://x.example.com/{{base}}\n# @gg-export base = regex: (.*)\n"},
		{name: "export comes after use", errLine: 1, errSub: "comes after this one", content: "GET https://x.example.com/{{base}}\n\n###\nGET https://x.example.com/cfg\n# @gg-export base = regex: (.*)\n"},
		{name: "uppercase scheme", content: "GET HTTPS://x.example.com/\n", wantOK: true},
		{name: "non-http scheme", content: "GET ftp://x.example.com/f\n", errLine: 1, errSub: "http:// or https://"},
		{name: "unknown method becomes url", content: "FETCH https://x.example.com\n", errLine: 1, errSub: "absolute"},
		{name: "undefined variable", content: "GET https://x.example.com/{{id}}\n", errLine: 1, errSub: "{{id}}"},
		{name: "unknown dynamic value", content: "GET https://x.example.com/?r={{$guid}}\n", errLine: 1, errSub: "{{$guid}}"},
		{name: "known dynamic value ok", content: "GET https://x.example.com/?r={{$uuid}}&t={{$timestamp}}&n={{$randomInt}}\n", wantOK: true},
		{name: "bad regex", errLine: 3, errSub: "invalid regex", content: `GET https://x.example.com/a
Accept: */*
# @gg-export v = regex: (unclosed
`},
		{name: "unknown engine", errLine: 2, errSub: "unknown extractor engine", content: `GET https://x.example.com/a
# @gg-export v = xpath: //a
`},
		{name: "malformed export line is not silently ignored", errLine: 2, errSub: "malformed @gg-export", content: `GET https://x.example.com/a
# @gg-export token jsonpath $.t
`},
		{name: "bad jsonpath index", errLine: 2, errSub: "invalid jsonpath", content: `GET https://x.example.com/a
# @gg-export v = jsonpath: $.items[abc].id
`},
		{name: "jsonpath doubled dot", errLine: 2, errSub: "empty path segment", content: `GET https://x.example.com/a
# @gg-export v = jsonpath: $.a..b
`},
		{name: "variable name that can never be referenced", errLine: 2, errSub: "can never be referenced", content: `GET https://x.example.com/a
# @gg-export my-token = jsonpath: $.t
`},
		{name: "empty block", errLine: 1, errSub: "no request line", content: "### just a name\n"},
		{name: "unused export is a warning", wantOK: true, warnLine: 2, warnSub: "no request uses", content: `GET https://x.example.com/a
# @gg-export v = jsonpath: $.t
`},
		{name: "missing blank line before body", wantOK: true, warnLine: 4, warnSub: "blank line", content: `POST https://x.example.com/a
Content-Type: application/json
Accept: */*
{"a":1}
`},
		{name: "invalid json body", wantOK: true, warnSub: "not valid JSON", content: `POST https://x.example.com/a
Content-Type: application/json

{"a":
`},
		{name: "json body with variables is not judged", wantOK: true, content: `POST https://x.example.com/a
Content-Type: application/json

{"t": {{$timestamp}}}
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Validate(c.content)
			if c.errSub == "" {
				if !r.OK {
					t.Fatalf("want ok, got errors %+v", r.Errors)
				}
			} else {
				if r.OK {
					t.Fatalf("want error containing %q, got ok", c.errSub)
				}
				if !hasIssue(r.Errors, c.errLine, c.errSub) {
					t.Fatalf("want error %q at line %d, got %+v", c.errSub, c.errLine, r.Errors)
				}
			}
			if c.warnSub != "" && !hasIssue(r.Warnings, c.warnLine, c.warnSub) {
				t.Fatalf("want warning %q at line %d, got %+v", c.warnSub, c.warnLine, r.Warnings)
			}
			if r.OK != (len(r.Errors) == 0) {
				t.Errorf("ok=%v inconsistent with %d errors", r.OK, len(r.Errors))
			}
		})
	}
}

func TestValidate_ChainBrokenByIndependentRequestHintsAtTheCause(t *testing.T) {
	r := Validate(`### login
POST https://x.example.com/login
# @gg-export token = jsonpath: $.t

### unrelated
GET https://x.example.com/health

### me
GET https://x.example.com/me
X-Token: {{token}}
`)
	if r.OK {
		t.Fatal("the unrelated request ends the chain, so {{token}} is undefined")
	}
	if !hasIssue(r.Errors, 9, "line 2 exports it") {
		t.Fatalf("want a hint naming the exporting line, got %+v", r.Errors)
	}
}

func TestValidate_ReportsJourneys(t *testing.T) {
	r := Validate(`### login
POST https://x.example.com/login
# @gg-export token = jsonpath: $.t

### me
GET https://x.example.com/me
X: {{token}}

### health
GET https://x.example.com/health
`)
	if !r.OK || r.Requests != 3 || len(r.Journeys) != 2 {
		t.Fatalf("ok=%v requests=%d journeys=%d errors=%+v", r.OK, r.Requests, len(r.Journeys), r.Errors)
	}
	first := r.Journeys[0]
	if len(first.Steps) != 2 || first.Steps[0].Exports[0] != "token" || first.Steps[1].Uses[0] != "token" ||
		first.Steps[0].Line != 2 || first.Steps[1].Line != 6 {
		t.Errorf("journey 1 = %+v", first)
	}
}

func TestValidate_ListsAreNeverNullInJSON(t *testing.T) {
	r := Validate("GET https://x.example.com/\n")
	if r.Errors == nil || r.Warnings == nil || r.Journeys == nil {
		t.Fatalf("errors/warnings/journeys must be empty slices, got %+v", r)
	}
}

func TestValidate_IssueFloodIsCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "GET /relative-%d\n\n###\n", i)
	}
	r := Validate(b.String())
	if len(r.Errors) != maxIssues || r.OmittedIssues == 0 {
		t.Fatalf("errors=%d omitted=%d", len(r.Errors), r.OmittedIssues)
	}
}

func TestValidate_ManyRequestsListingIsCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&b, "### r%d\nGET https://x.example.com/%d\n\n", i, i)
	}
	r := Validate(b.String())
	listed := 0
	for _, j := range r.Journeys {
		listed += len(j.Steps)
	}
	if r.Requests != 150 || listed != maxListedStep || r.OmittedRequests != 50 {
		t.Fatalf("requests=%d listed=%d omitted=%d", r.Requests, listed, r.OmittedRequests)
	}
}

// What validate accepts must run the same way through the real parser/grouping.
func TestValidate_AgreesWithEngineGrouping(t *testing.T) {
	content := `### a
GET https://x.example.com/a
# @gg-export id = jsonpath: $.id

### b
GET https://x.example.com/b/{{id}}

### c
GET https://x.example.com/c
`
	plan, err := NewPlan(content)
	if err != nil {
		t.Fatal(err)
	}
	r := Validate(content)
	if len(plan) != len(r.Journeys) {
		t.Fatalf("plan has %d journeys, validate reports %d", len(plan), len(r.Journeys))
	}
	for i := range plan {
		if len(plan[i].Steps) != len(r.Journeys[i].Steps) {
			t.Errorf("journey %d: plan %d steps, validate %d", i, len(plan[i].Steps), len(r.Journeys[i].Steps))
		}
	}
}
