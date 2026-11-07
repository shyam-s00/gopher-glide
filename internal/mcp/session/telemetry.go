package session

import (
	"fmt"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/ui"
)

// Metrics are the live numbers from the latest heartbeat.
type Metrics struct {
	TargetRPS int     `json:"target_rps"`
	ActualRPS float64 `json:"actual_rps"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	ErrorRate float64 `json:"error_rate"`
	Total     int64   `json:"total_requests"`
	Success   int64   `json:"success_count"`
	Failure   int64   `json:"failure_count"`
	Bias      int     `json:"bias"`
	// AgeS is how old this reading is.
	AgeS float64 `json:"age_s"`
}

// Final is the run's closing statistics.
type Final struct {
	ErrorRate float64 `json:"error_rate"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	Total     int64   `json:"total_requests"`
	Success   int64   `json:"success_count"`
	Failure   int64   `json:"failure_count"`
}

// Telemetry is the compact, size-bounded view of a session an agent polls.
type Telemetry struct {
	SessionID string `json:"session_id"`
	State     State  `json:"state"`
	// ElapsedS counts from the moment gg reported "started", the same zero
	// point a steering mark's elapsed offset uses.
	ElapsedS float64          `json:"elapsed_s"`
	PlannedS float64          `json:"planned_s,omitempty"`
	Stage    string           `json:"stage,omitempty"`
	Metrics  *Metrics         `json:"metrics,omitempty"`
	ErrorMix *engine.ErrorMix `json:"error_mix,omitempty"`
	Final    *Final           `json:"final,omitempty"`
	Marks    []MarkInfo       `json:"recent_marks,omitempty"`

	SnapshotPath string `json:"snapshot_path,omitempty"`
	Message      string `json:"message,omitempty"`
	StderrTail   string `json:"stderr_tail,omitempty"`
}

const (
	maxStderrInTelemetry = 512
	maxMessageBytes      = 200
)

// Telemetry returns the session's current view. It never blocks on the child.
func (s *Session) Telemetry() Telemetry {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := Telemetry{SessionID: s.ID, State: s.state, SnapshotPath: s.snapPath}
	if len(s.marks) > 0 {
		t.Marks = append([]MarkInfo(nil), s.marks...)
	}
	t.PlannedS = round2(s.spec.Planned.Seconds())
	if !s.t0.IsZero() {
		end := time.Now()
		if !s.endedAt.IsZero() {
			end = s.endedAt
		}
		t.ElapsedS = round2(end.Sub(s.t0).Seconds())
	}

	switch {
	case s.state == Failed:
		t.Message = clip(s.failure, maxMessageBytes)
		t.StderrTail = tailOf(s.stderr.String(), maxStderrInTelemetry)
	case s.terminal != nil:
		f := s.terminal
		t.Final = &Final{
			ErrorRate: round4(f.ErrorRate), P50Ms: round2(f.P50Ms), P95Ms: round2(f.P95Ms), P99Ms: round2(f.P99Ms),
			Total: f.TotalReqs, Success: f.SuccessCount, Failure: f.FailureCount,
		}
		switch {
		case s.state == Stopped && s.stoppedEarly(t.ElapsedS):
			t.Message = fmt.Sprintf("stopped early at %.0fs of %.0fs planned; the final statistics and any snapshot cover only that part",
				t.ElapsedS, t.PlannedS)
		case s.snapPath == "":
			t.Message = clip(f.Message, maxMessageBytes)
		}
	case s.lastBeat != nil:
		t.Metrics = metricsOf(s.lastBeat, time.Since(s.lastBeatAt))
		t.ErrorMix = s.lastBeat.ErrorMix
		t.Stage = s.stageLabel(s.lastBeat)
	}
	return t
}

func metricsOf(b *ui.HeartbeatPayload, age time.Duration) *Metrics {
	m := &Metrics{
		TargetRPS: b.TargetRPS, ActualRPS: round2(b.ActualRPS),
		P50Ms: round2(b.P50Ms), P95Ms: round2(b.P95Ms), P99Ms: round2(b.P99Ms),
		ErrorRate: round4(b.ErrorRate),
		Total:     b.TotalReqs, Success: b.SuccessCount, Failure: b.FailureCount,
		AgeS: round2(age.Seconds()),
	}
	if b.Bias != nil {
		m.Bias = *b.Bias
	}
	return m
}

// stageLabel renders "2/5 Sustain"; the caller holds s.mu.
func (s *Session) stageLabel(b *ui.HeartbeatPayload) string {
	if b.TotalStages == 0 {
		return ""
	}
	label := strconv.Itoa(b.Stage) + "/" + strconv.Itoa(b.TotalStages)
	if s.started != nil && b.Stage >= 1 && b.Stage <= len(s.started.Stages) {
		label += " " + clip(s.started.Stages[b.Stage-1].Name, 40)
	}
	return label
}

// Started returns what the child reported on "started" (stages, capabilities).
func (s *Session) Started() (stages []ui.StageInfo, capabilities []string, protocol int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started == nil {
		return nil, nil, 0, false
	}
	if s.started.Capabilities != nil {
		capabilities = append([]string(nil), *s.started.Capabilities...)
	}
	return append([]ui.StageInfo(nil), s.started.Stages...), capabilities, s.started.ProtocolVersion, true
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// clip cuts s to at most max bytes on a rune boundary, marking the cut.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// tailOf keeps the last max bytes of s on a rune boundary.
func tailOf(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "…" + s[cut:]
}

// earlyStopSlack absorbs stage rounding so a run that ends on schedule is not
// reported as cut short.
const earlyStopSlack = 2.0

// stoppedEarly reports whether the run ended well before its planned length.
func (s *Session) stoppedEarly(elapsed float64) bool {
	planned := s.spec.Planned.Seconds()
	return planned > 0 && elapsed < planned-earlyStopSlack
}
