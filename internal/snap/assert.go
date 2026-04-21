package snap

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// AssertOptions configures the thresholds and behaviour of Assert().
// It extends DiffOptions with CI-specific controls.
type AssertOptions struct {
	DiffOptions

	// FailOnWarn treats WARN verdicts as failures in addition to REGRESSION.
	// Default: false (only REGRESSION causes a non-zero exit).
	FailOnWarn bool
}

// DefaultAssertOptions returns AssertOptions with sensible CI defaults.
func DefaultAssertOptions() AssertOptions {
	return AssertOptions{
		DiffOptions: DefaultDiffOptions(),
	}
}

// Violation describes a single threshold breach found by Assert().
type Violation struct {
	EndpointID string      `json:"endpoint_id"`
	Verdict    DiffVerdict `json:"verdict"`
	// Message is a human-readable, actionable description of the breach.
	// Example: "P99 latency +38.4% (threshold: 10%)"
	Message string `json:"message"`
}

// AssertResult is returned by Assert().
type AssertResult struct {
	Passed     bool        `json:"passed"`
	Violations []Violation `json:"violations"`
	DiffResult DiffResult  `json:"diff"`
}

// Assert compares baseline and current snapshots and returns an AssertResult.
// Passed is true only when no threshold-crossing Violations are found.
func Assert(baseline, current *Snapshot, opts AssertOptions) AssertResult {
	diff := Diff(baseline, current, opts.DiffOptions)
	result := AssertResult{DiffResult: diff}

	for _, ep := range diff.Endpoints {
		isFailure := ep.Verdict == VerdictRegression ||
			(opts.FailOnWarn && ep.Verdict == VerdictWarn)
		if !isFailure {
			continue
		}

		msgs := violationMessages(ep, opts.DiffOptions)
		for _, msg := range msgs {
			result.Violations = append(result.Violations, Violation{
				EndpointID: ep.ID,
				Verdict:    ep.Verdict,
				Message:    msg,
			})
		}

		// If the endpoint produced a verdict but no specific threshold message
		// (e.g. it's baseline-only / current-only), add a generic entry.
		if len(msgs) == 0 {
			label := "removed (baseline-only)"
			if ep.CurrentOnly {
				label = "added (current-only)"
			}
			result.Violations = append(result.Violations, Violation{
				EndpointID: ep.ID,
				Verdict:    ep.Verdict,
				Message:    label,
			})
		}
	}

	result.Passed = len(result.Violations) == 0
	return result
}

// violationMessages returns the set of human-readable messages for a single
// endpoint diff, covering each individual threshold that was breached.
func violationMessages(ep EndpointDiff, opts DiffOptions) []string {
	var msgs []string

	// P99 latency regression.
	if ep.LatencyDelta.P99PctChange > opts.LatencyP99RegressionPct {
		msgs = append(msgs, fmt.Sprintf(
			"P99 latency %+.1f%% (threshold: %.0f%%)",
			ep.LatencyDelta.P99PctChange, opts.LatencyP99RegressionPct,
		))
	}

	// Error rate spike.
	if ep.ErrorRateDelta > opts.ErrorRateDeltaThreshold {
		msgs = append(msgs, fmt.Sprintf(
			"error rate %+.2f pp (threshold: %.2f pp)",
			ep.ErrorRateDelta*100, opts.ErrorRateDeltaThreshold*100,
		))
	}

	// Schema changes.
	for _, fc := range ep.SchemaChanges {
		switch fc.Kind {
		case FieldTypeChanged:
			msgs = append(msgs, fmt.Sprintf(
				"field %q type changed %s→%s",
				fc.Path, fc.BaseType, fc.CurrType,
			))
		case FieldRemoved:
			if opts.DenyRemovedFields {
				msgs = append(msgs, fmt.Sprintf("field %q removed", fc.Path))
			}
		}
	}

	// Payload size growth (WARN-level — included only if FailOnWarn or already REGRESSION).
	if ep.PayloadDelta.AvgPctChange > opts.PayloadSizeAvgPctThreshold {
		msgs = append(msgs, fmt.Sprintf(
			"avg payload size %+.1f%% (threshold: %.0f%%)",
			ep.PayloadDelta.AvgPctChange, opts.PayloadSizeAvgPctThreshold,
		))
	}

	return msgs
}

// ── Reporters ─────────────────────────────────────────────────────────────────

// FormatAssertResult serialises an AssertResult to the requested format.
// Supported formats: "text" (default), "json", "md".
func FormatAssertResult(r AssertResult, format string) (string, error) {
	switch strings.ToLower(format) {
	case "json":
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	case "md", "markdown":
		return formatAssertMarkdown(r), nil
	default:
		return formatAssertText(r), nil
	}
}

func formatAssertText(r AssertResult) string {
	var sb strings.Builder

	status := "✅ PASSED"
	if !r.Passed {
		status = "❌ FAILED"
	}
	sb.WriteString(fmt.Sprintf("Assert Result: %s\n", status))
	sb.WriteString(fmt.Sprintf("Baseline : %s  (%s)\n",
		displayTag(r.DiffResult.Baseline.Tag), r.DiffResult.Baseline.StartTime.Format("2006-01-02 15:04:05Z07:00")))
	sb.WriteString(fmt.Sprintf("Current  : %s  (%s)\n",
		displayTag(r.DiffResult.Current.Tag), r.DiffResult.Current.StartTime.Format("2006-01-02 15:04:05Z07:00")))

	// Summary counts.
	pass, warn, reg := tallyVerdicts(r.DiffResult)
	sb.WriteString(fmt.Sprintf("Endpoints: %d total  (%d PASS  %d WARN  %d REGRESSION)\n",
		pass+warn+reg, pass, warn, reg))

	if len(r.Violations) == 0 {
		sb.WriteString("\nNo threshold violations detected.\n")
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("\nViolations (%d):\n", len(r.Violations)))
	for _, v := range r.Violations {
		sb.WriteString(fmt.Sprintf("  [%s] %s — %s\n", v.Verdict, v.EndpointID, v.Message))
	}
	return sb.String()
}

func formatAssertMarkdown(r AssertResult) string {
	var sb strings.Builder

	status := "✅ **PASSED**"
	if !r.Passed {
		status = "❌ **FAILED**"
	}
	sb.WriteString(fmt.Sprintf("## `gg snap assert` Result: %s\n\n", status))

	sb.WriteString("| | |\n|---|---|\n")
	sb.WriteString(fmt.Sprintf("| Baseline | `%s` — %s |\n",
		displayTag(r.DiffResult.Baseline.Tag),
		r.DiffResult.Baseline.StartTime.Format("2006-01-02 15:04 UTC")))
	sb.WriteString(fmt.Sprintf("| Current | `%s` — %s |\n\n",
		displayTag(r.DiffResult.Current.Tag),
		r.DiffResult.Current.StartTime.Format("2006-01-02 15:04 UTC")))

	pass, warn, reg := tallyVerdicts(r.DiffResult)
	sb.WriteString(fmt.Sprintf(
		"**Endpoints**: %d total &nbsp;·&nbsp; %d PASS &nbsp;·&nbsp; %d WARN &nbsp;·&nbsp; %d REGRESSION\n\n",
		pass+warn+reg, pass, warn, reg,
	))

	if len(r.Violations) == 0 {
		sb.WriteString("No threshold violations detected.\n")
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("### Violations (%d)\n\n", len(r.Violations)))
	sb.WriteString("| Verdict | Endpoint | Detail |\n|---|---|---|\n")
	for _, v := range r.Violations {
		icon := "🟡"
		if v.Verdict == VerdictRegression {
			icon = "🔴"
		}
		sb.WriteString(fmt.Sprintf("| %s %s | `%s` | %s |\n",
			icon, v.Verdict, v.EndpointID, v.Message))
	}
	sb.WriteString("\n")

	// Per-endpoint details table.
	sb.WriteString("### Per-Endpoint Summary\n\n")
	sb.WriteString("| Verdict | Endpoint | P99 Δ | Err Rate Δ | Payload Avg Δ |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for _, ep := range r.DiffResult.Endpoints {
		icon := verdictIcon(ep.Verdict)
		p99 := fmtPct(ep.LatencyDelta.P99PctChange)
		errD := fmtPP(ep.ErrorRateDelta)
		payD := fmtPct(ep.PayloadDelta.AvgPctChange)
		sb.WriteString(fmt.Sprintf("| %s %s | `%s` | %s | %s | %s |\n",
			icon, ep.Verdict, ep.ID, p99, errD, payD))
	}
	return sb.String()
}

// ── helpers ───────────────────────────────────────────────────────────────────

func displayTag(tag string) string {
	if tag == "" || tag == "run" {
		return "(untagged)"
	}
	return tag
}

func tallyVerdicts(r DiffResult) (pass, warn, reg int) {
	for _, ep := range r.Endpoints {
		switch ep.Verdict {
		case VerdictPass:
			pass++
		case VerdictWarn:
			warn++
		case VerdictRegression:
			reg++
		}
	}
	return
}

func verdictIcon(v DiffVerdict) string {
	switch v {
	case VerdictRegression:
		return "🔴"
	case VerdictWarn:
		return "🟡"
	default:
		return "🟢"
	}
}

func fmtPct(v float64) string {
	if math.Abs(v) < 0.05 {
		return "—"
	}
	if v > 0 {
		return fmt.Sprintf("+%.1f%%", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}

func fmtPP(v float64) string {
	pp := v * 100
	if math.Abs(pp) < 0.005 {
		return "—"
	}
	if pp > 0 {
		return fmt.Sprintf("+%.2f pp", pp)
	}
	return fmt.Sprintf("%.2f pp", pp)
}
