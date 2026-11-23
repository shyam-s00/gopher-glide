package mcp

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/profile"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func readResource(t *testing.T, cs *sdk.ClientSession, uri string) string {
	t.Helper()
	res, err := cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("read %s: %v", uri, err)
	}
	if len(res.Contents) != 1 || res.Contents[0].Text == "" {
		t.Fatalf("read %s: want one non-empty text content, got %+v", uri, res.Contents)
	}
	if res.Contents[0].MIMEType != markdown || res.Contents[0].URI != uri {
		t.Errorf("read %s: mime/uri = %q/%q", uri, res.Contents[0].MIMEType, res.Contents[0].URI)
	}
	return res.Contents[0].Text
}

func TestResources_ListedAndReadable(t *testing.T) {
	cs := connect(t)
	list, err := cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range list.Resources {
		got[r.URI] = true
		if r.Description == "" {
			t.Errorf("%s has no description", r.URI)
		}
	}
	for _, uri := range []string{URIHTTPSyntax, URIProfileCatalog, URIConfigSchema} {
		if !got[uri] {
			t.Errorf("resource %s not listed", uri)
		}
		readResource(t, cs, uri)
	}
}

func TestResources_UnknownURIIsAnError(t *testing.T) {
	cs := connect(t)
	if _, err := cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "gg://docs/nope"}); err == nil {
		t.Fatal("unknown resource must fail")
	}
}

// A profile added without a description would give agents a blank catalog entry.
func TestProfileCatalog_CoversEveryBuiltInWithDescription(t *testing.T) {
	names := profile.ListNames()
	if len(names) == 0 {
		t.Fatal("no built-in profiles found")
	}
	catalog, err := profileCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		p, err := profile.LoadBuiltIn(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("profile %q has no description; add one to its YAML", name)
		}
		section := "## " + p.Name + "\n"
		i := strings.Index(catalog, section)
		if i < 0 {
			t.Errorf("catalog is missing profile %q", name)
			continue
		}
		rest := catalog[i+len(section):]
		if j := strings.Index(rest, "\n## "); j >= 0 {
			rest = rest[:j]
		}
		if !strings.Contains(rest, strings.TrimSpace(p.Description)) {
			t.Errorf("catalog entry for %q lacks its description", name)
		}
		if !strings.Contains(rest, "Default:") || !strings.Contains(rest, "Shape:") {
			t.Errorf("catalog entry for %q lacks default/shape lines", name)
		}
	}
	if got := strings.Count(catalog, "\n## "); got != len(names) {
		t.Errorf("catalog has %d entries, registry has %d", got, len(names))
	}
}

func TestProfileCatalog_ServedThroughResource(t *testing.T) {
	cs := connect(t)
	text := readResource(t, cs, URIProfileCatalog)
	for _, name := range profile.ListNames() {
		if !strings.Contains(text, "## "+name+"\n") {
			t.Errorf("served catalog missing %q", name)
		}
	}
}

var httpBlock = regexp.MustCompile("(?s)```http\n(.*?)```")

// Every example in the syntax doc must parse with the real parser.
func TestHTTPSyntaxDoc_ExamplesParse(t *testing.T) {
	doc, err := docsFS.ReadFile("docs/http-syntax.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := httpBlock.FindAllStringSubmatch(string(doc), -1)
	if len(blocks) < 3 {
		t.Fatalf("expected at least 3 http examples, found %d", len(blocks))
	}
	for i, b := range blocks {
		specs, err := httpreader.Parse(b[1])
		if err != nil || len(specs) == 0 {
			t.Errorf("example %d: specs=%d err=%v\n%s", i, len(specs), err, b[1])
		}
	}
}

func TestHTTPSyntaxDoc_ChainingExampleFormsOneJourney(t *testing.T) {
	doc, _ := docsFS.ReadFile("docs/http-syntax.md")
	for _, b := range httpBlock.FindAllStringSubmatch(string(doc), -1) {
		if !strings.Contains(b[1], "@gg-export") {
			continue
		}
		specs, err := httpreader.Parse(b[1])
		if err != nil {
			t.Fatal(err)
		}
		journeys := httpreader.GroupJourneys(specs)
		if len(journeys) != 1 || len(journeys[0].Specs) != 2 {
			t.Fatalf("login example should be one two-step journey, got %d journeys", len(journeys))
		}
		if d := journeys[0].Specs[0].Exports[0]; d.VarName != "token" || d.Engine != httpreader.ExportEngineJSONPath {
			t.Errorf("export = %+v", d)
		}
		return
	}
	t.Fatal("no @gg-export example in the doc")
}

func TestHTTPSyntaxDoc_MentionsEveryEngineAndDynamicValue(t *testing.T) {
	doc, _ := docsFS.ReadFile("docs/http-syntax.md")
	for _, want := range []string{
		string(httpreader.ExportEngineJSONPath), string(httpreader.ExportEngineRegex),
		"{{$uuid}}", "{{$randomInt}}", "{{$timestamp}}", "@gg-export",
	} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("http-syntax doc does not mention %q", want)
		}
	}
}

// Keys the config accepts but does not act on yet are left out of the doc.
var undocumentedConfigKeys = map[string]bool{
	"breaker_threshold_pct": true,
	"prometheus":            true,
	"prometheus_port":       true,
}

func yamlKeys(t reflect.Type, out map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if key == "" || key == "-" {
			continue
		}
		out[key] = true
		ft := f.Type
		if ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() == t.PkgPath() {
			yamlKeys(ft, out)
		}
	}
}

// Adding a config key must force a decision: document it or list it as unused.
func TestConfigDoc_CoversEveryYAMLKey(t *testing.T) {
	doc, err := docsFS.ReadFile("docs/gg-yaml.md")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	yamlKeys(reflect.TypeOf(config.Config{}), keys)
	if len(keys) < 10 {
		t.Fatalf("reflection found only %d keys", len(keys))
	}
	for k := range keys {
		documented := strings.Contains(string(doc), k+":")
		switch {
		case undocumentedConfigKeys[k] && documented:
			t.Errorf("%q is documented but listed as unused", k)
		case !undocumentedConfigKeys[k] && !documented:
			t.Errorf("config key %q is missing from docs/gg-yaml.md", k)
		}
	}
}

// The sample config in the doc must load and validate.
func TestConfigDoc_ExampleLoads(t *testing.T) {
	doc, _ := docsFS.ReadFile("docs/gg-yaml.md")
	m := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindStringSubmatch(string(doc))
	if m == nil {
		t.Fatal("no yaml example in the doc")
	}
	dir := t.TempDir()
	path := dir + "/gg.yaml"
	if err := writeFile(path, m[1]); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("doc example does not load: %v", err)
	}
	if len(cfg.Stages) != 4 {
		t.Errorf("stages = %d, want 4", len(cfg.Stages))
	}
}
