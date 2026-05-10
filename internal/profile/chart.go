package profile

import (
	"fmt"
	"strings"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
)

const (
	chartWidth  = 60
	chartHeight = 10
)

// RenderASCIIChart renders a 2D ASCII bar chart of the RPS traffic shape
// defined by stages. The X-axis represents time and the Y-axis represents
// RPS relative to the peak.
//
// Block characters used:
//
//	█  full block       (row is fully filled)
//	▄  lower-half block (top edge sub-row precision)
//	   space            (row is empty)
//
// Example output (flash-sale shape):
//
//	2000 │████████████████████████████████████████████████████████████
//	     │████████████████████████████████████████████████████████████
//	     │████████████████████████████████████████████████████████████
//	   0 └────────────────────────────────────────────────────────────
//	      0s                            1m30s                       3m
func RenderASCIIChart(stages []config.Stage) string {
	if len(stages) == 0 {
		return ""
	}

	peak := peakRPS(stages)
	if peak == 0 {
		return ""
	}

	// Sample RPS at chartWidth equally-spaced time buckets.
	samples := sampleRPS(stages, chartWidth)

	peakLabel := fmt.Sprintf("%d", peak)
	labelW := len(peakLabel) + 1 // extra space between label and border

	var sb strings.Builder

	for row := 0; row < chartHeight; row++ {
		// Y-axis label: show peak on first row, 0 on last, blank otherwise.
		switch row {
		case 0:
			sb.WriteString(fmt.Sprintf("%*d │", labelW, peak))
		case chartHeight - 1:
			sb.WriteString(fmt.Sprintf("%*d │", labelW, 0))
		default:
			sb.WriteString(fmt.Sprintf("%*s │", labelW, ""))
		}

		for _, rps := range samples {
			// filledF is the number of rows that should be filled (from bottom),
			// expressed as a float so we can use the fractional part for half-blocks.
			filledF := rps / float64(peak) * float64(chartHeight)

			// rowFromBottom counts up from 0 (row chartHeight-1) to chartHeight-1 (row 0).
			rowFromBottom := float64(chartHeight - 1 - row)

			switch {
			case rowFromBottom < filledF-0.5:
				// Well below the top edge — full block.
				sb.WriteRune('█')
			case rowFromBottom < filledF:
				// At the top edge — lower-half block for sub-row precision.
				sb.WriteRune('▄')
			default:
				sb.WriteRune(' ')
			}
		}
		sb.WriteRune('\n')
	}

	// Bottom border.
	sb.WriteString(fmt.Sprintf("%*s └%s\n", labelW, "", strings.Repeat("─", chartWidth)))

	// Time axis labels.
	totalDur := totalNonZeroDuration(stages)
	sb.WriteString(buildTimeAxis(labelW+2, chartWidth, totalDur))
	return sb.String()
}

// buildTimeAxis returns a single line with start / mid / end time labels
// placed at their respective horizontal positions.
func buildTimeAxis(offset, width int, total time.Duration) string {
	startLabel := "0s"
	midLabel := FormatDuration(total / 2)
	endLabel := FormatDuration(total)

	// Build a byte slice pre-filled with spaces.
	lineLen := offset + width
	line := make([]byte, lineLen)
	for i := range line {
		line[i] = ' '
	}

	// Start label at the left edge of the chart area.
	copy(line[offset:], startLabel)

	// Mid label centred, only if it doesn't collide with start label.
	midPos := offset + (width-len(midLabel))/2
	if midPos > offset+len(startLabel) {
		copy(line[midPos:], midLabel)
	}

	// End label flush with the right edge, only if it doesn't collide with mid.
	endPos := offset + width - len(endLabel)
	if endPos > midPos+len(midLabel) {
		copy(line[endPos:], endLabel)
	}

	return strings.TrimRight(string(line), " ") + "\n"
}

// sampleRPS returns n uniformly-spaced RPS values representing the traffic
// shape across the total duration.  The i-th sample is taken at the centre of
// its column bucket: t = (i + 0.5) / n.
func sampleRPS(stages []config.Stage, n int) []float64 {
	samples := make([]float64, n)
	total := totalNonZeroDuration(stages)

	if total == 0 {
		// All instant stages — render a flat bar at the peak level.
		peak := float64(peakRPS(stages))
		for i := range samples {
			samples[i] = peak
		}
		return samples
	}

	for i := 0; i < n; i++ {
		t := (float64(i) + 0.5) / float64(n)
		samples[i] = rpsAtFraction(stages, total, t)
	}
	return samples
}

// rpsAtFraction returns the interpolated RPS at fraction t ∈ [0, 1] of
// totalDur, walking the stage list and applying lerp within each stage.
// Instant stages (Duration == 0) are treated as point-in-time jumps.
func rpsAtFraction(stages []config.Stage, totalDur time.Duration, t float64) float64 {
	target := time.Duration(float64(totalDur) * t)
	prevRPS := 0.0
	elapsed := time.Duration(0)

	for _, s := range stages {
		if s.Duration == 0 {
			// Instant jump at time `elapsed`. Only apply it if we have
			// already passed (or are exactly at) this point in time.
			if target > elapsed {
				prevRPS = float64(s.TargetRPS)
			}
			continue
		}

		stageEnd := elapsed + s.Duration
		if target <= stageEnd {
			frac := float64(target-elapsed) / float64(s.Duration)
			return prevRPS + frac*(float64(s.TargetRPS)-prevRPS)
		}

		elapsed = stageEnd
		prevRPS = float64(s.TargetRPS)
	}

	return prevRPS
}

// peakRPS returns the maximum TargetRPS across all stages.
func peakRPS(stages []config.Stage) int {
	peak := 0
	for _, s := range stages {
		if s.TargetRPS > peak {
			peak = s.TargetRPS
		}
	}
	return peak
}

// totalNonZeroDuration returns the sum of all non-instant stage durations.
func totalNonZeroDuration(stages []config.Stage) time.Duration {
	var total time.Duration
	for _, s := range stages {
		total += s.Duration
	}
	return total
}

// FormatDuration converts a duration to a compact human-readable label.
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%gs", d.Seconds())
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%gm", d.Minutes())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", m, s)
}
