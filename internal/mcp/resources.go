package mcp

import (
	"context"
	"embed"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shyam-s00/gopher-glide/internal/profile"
)

// Resource URIs served by the server.
const (
	URIHTTPSyntax     = "gg://docs/http-syntax"
	URIProfileCatalog = "gg://profiles/catalog"
	URIConfigSchema   = "gg://schema/gg-yaml"
)

//go:embed docs/http-syntax.md docs/gg-yaml.md
var docsFS embed.FS

const markdown = "text/markdown"

func registerResources(s *sdk.Server) error {
	httpDoc, err := docsFS.ReadFile("docs/http-syntax.md")
	if err != nil {
		return err
	}
	cfgDoc, err := docsFS.ReadFile("docs/gg-yaml.md")
	if err != nil {
		return err
	}
	catalog, err := profileCatalog()
	if err != nil {
		return err
	}

	add := func(uri, name, desc, body string) {
		s.AddResource(&sdk.Resource{URI: uri, Name: name, Description: desc, MIMEType: markdown},
			func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
				return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{
					{URI: req.Params.URI, MIMEType: markdown, Text: body},
				}}, nil
			})
	}
	add(URIHTTPSyntax, "http-syntax",
		"How to write a gg .http file: requests, headers, bodies, dynamic values and @gg-export chaining.", string(httpDoc))
	add(URIProfileCatalog, "profile-catalog",
		"Built-in traffic profiles with default peak RPS, duration, shape and when to use each.", catalog)
	add(URIConfigSchema, "gg-yaml",
		"Reference for the gg YAML config file: config, snap and stages sections plus validation rules.", string(cfgDoc))
	return nil
}

// profileCatalog renders the built-in profiles from the registry itself, so it
// cannot drift from what `--profile` accepts.
func profileCatalog() (string, error) {
	var b strings.Builder
	b.WriteString("# gg traffic profiles\n\n")
	b.WriteString("A profile is a reusable traffic shape. It is scaled to a peak RPS and a total duration ")
	b.WriteString("when a simulation starts; the defaults below apply when you give neither.\n\n")
	for _, name := range profile.ListNames() {
		p, err := profile.LoadBuiltIn(name)
		if err != nil {
			return "", fmt.Errorf("load profile %q: %w", name, err)
		}
		fmt.Fprintf(&b, "## %s\n\n", p.Name)
		fmt.Fprintf(&b, "Default: %d RPS peak over %s.\n", p.DefaultPeakRPS, profile.FormatDuration(p.DefaultDuration))
		fmt.Fprintf(&b, "Shape: %s.\n\n", describeShape(p))
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(p.Description))
	}
	return b.String(), nil
}

func describeShape(p *profile.Profile) string {
	parts := make([]string, len(p.Segments))
	for i, seg := range p.Segments {
		parts[i] = fmt.Sprintf("%s to %.0f%% (%.0f%% of run)", seg.Type, seg.RPSMultiplier*100, seg.DurationPct*100)
	}
	return strings.Join(parts, " → ")
}
