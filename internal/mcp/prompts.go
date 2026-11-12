package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type promptDef struct {
	prompt *sdk.Prompt
	build  func(args map[string]string) string
}

func promptDefs() []promptDef {
	return []promptDef{
		{
			prompt: &sdk.Prompt{
				Name:        "author_journey",
				Title:       "Write and verify a load-test journey",
				Description: "Write a .http file for an API, validate it and dry-run it before any load is generated.",
				Arguments: []*sdk.PromptArgument{
					{Name: "base_url", Description: "Base URL of the API under test", Required: true},
					{Name: "goal", Description: "What user flow or endpoints to cover"},
				},
			},
			build: func(a map[string]string) string {
				return fmt.Sprintf(`Write a gg load-test file for the API at %s.%s

1. Read the gg://docs/http-syntax resource first.
2. Write a .http file: one request per endpoint, and chain requests with @gg-export when a later call needs a value from an earlier response.
3. Call validate_http_file and fix every reported error until it returns ok.
4. Call run_journey_dry_run to execute each journey once against the real API. Fix any failing step using the status and body preview it reports.
5. Call generate_config to produce a config for the run.
Do not call start_simulation until the user has confirmed the file and the target.`, a["base_url"], goalLine(a["goal"]))
			},
		},
		{
			prompt: &sdk.Prompt{
				Name:        "find_ceiling",
				Title:       "Ramp load to find the capacity ceiling",
				Description: "Start a run, raise traffic in steps while watching latency and errors, and report where the service degrades.",
				Arguments: []*sdk.PromptArgument{
					{Name: "http_file", Description: "Path to the validated .http file", Required: true},
					{Name: "max_rps", Description: "Highest RPS you may try (the server's own cap still applies)"},
				},
			},
			build: func(a map[string]string) string {
				limit := "the server's cap"
				if a["max_rps"] != "" {
					limit = a["max_rps"] + " RPS"
				}
				return fmt.Sprintf(`Find the capacity ceiling of the service described by %s, without going above %s.

1. Read gg://profiles/catalog and start a gentle run with start_simulation (a low-RPS profile or a short ramp).
2. Watch with get_live_telemetry using wait_s (for example 5) and wake_on_p99_ms or wake_on_error_rate, so each call returns as soon as something changes instead of polling. Judge health on the window figures: p99 stable and error rate below 1%%.
3. While healthy, call steer_traffic with adjust_rps to raise traffic by about 20%%, and wait for the change to show in telemetry (it applies within about a second). To judge a rate on its own, hold_rps pins it flat until release_hold.
4. When p99 roughly doubles or errors exceed 1%%, call steer_traffic with a mark describing what you saw (error_mix.top_endpoints names the failing endpoints), lower the load, and confirm recovery in the window figures.
5. Call stop_simulation and report the highest healthy RPS, what degraded first, and the evidence from telemetry.
Never leave a simulation running when you finish.`, a["http_file"], limit)
			},
		},
		{
			prompt: &sdk.Prompt{
				Name:        "regression_check",
				Title:       "Compare a run against a baseline",
				Description: "Run the same journeys, capture a snapshot and report behavioural regressions against a baseline snapshot.",
				Arguments: []*sdk.PromptArgument{
					{Name: "http_file", Description: "Path to the validated .http file", Required: true},
					{Name: "baseline", Description: "Path or tag of the baseline snapshot", Required: true},
				},
			},
			build: func(a map[string]string) string {
				return fmt.Sprintf(`Check %s for regressions against the baseline snapshot %s. If the baseline is not at hand, snap_list shows the saved snapshots, newest first.

1. Call start_simulation with snapshot capture enabled and a short, moderate profile that matches how the baseline was recorded.
2. Watch it with get_live_telemetry, then call stop_simulation to finish and obtain the new snapshot.
3. Call snap_capture on the new snapshot to confirm it recorded the expected endpoints.
4. Call snap_diff between the baseline and the new snapshot, and snap_assert for a pass/fail verdict.
5. Report each regression with the endpoint, what changed and by how much. If marks or bias changes are listed, say the comparison may include manual steering.`, a["http_file"], a["baseline"])
			},
		},
	}
}

func goalLine(goal string) string {
	if strings.TrimSpace(goal) == "" {
		return ""
	}
	return " Goal: " + strings.TrimSpace(goal) + "."
}

func registerPrompts(s *sdk.Server) {
	for _, d := range promptDefs() {
		s.AddPrompt(d.prompt, func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			args := req.Params.Arguments
			for _, a := range d.prompt.Arguments {
				if a.Required && strings.TrimSpace(args[a.Name]) == "" {
					return nil, fmt.Errorf("missing required argument %q", a.Name)
				}
			}
			return &sdk.GetPromptResult{
				Description: d.prompt.Description,
				Messages: []*sdk.PromptMessage{{
					Role:    "user",
					Content: &sdk.TextContent{Text: d.build(args)},
				}},
			}, nil
		})
	}
}
