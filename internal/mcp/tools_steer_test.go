package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/mcp/session"
)

func reading(target, biasAtReading, bias int) session.Reading {
	return session.Reading{Available: true, TargetRPS: target, BiasAtReading: biasAtReading, Bias: bias, Settled: true, PeakRPS: 100}
}

func TestPlanAdjust_Table(t *testing.T) {
	l := Limits{MaxRPS: 500}
	cases := []struct {
		name    string
		r       session.Reading
		delta   int
		want    int
		wantErr string
		note    string
	}{
		{"raise", reading(50, 0, 0), 40, 40, "", "within about 1 s"},
		{"lower", reading(50, 0, 0), -20, -20, "", ""},
		{"up to the cap exactly", reading(50, 0, 0), 400, 400, "", ""},
		{"over the cap", reading(50, 0, 0), 401, 0, "--max-rps", ""},
		{"cap counts confirmed bias not the lagging heartbeat", reading(50, 0, 300), 101, 0, "above the server limit", ""},
		{"floor is called out", reading(10, 0, 0), -50, -50, "", "below 1 RPS"},
		{"no reading yet", session.Reading{}, 5, 0, "no telemetry reading yet", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, note, err := planAdjust(l, c.delta)(c.r)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want || !strings.Contains(note, c.note) {
				t.Fatalf("got %d %q %v", got, note, err)
			}
		})
	}
}

func TestPlanSet_Table(t *testing.T) {
	l := Limits{MaxRPS: 500}
	stale := reading(50, 0, 0)
	stale.Age = 10 * time.Second
	unsettled := reading(50, 0, 5)
	unsettled.Settled = false
	floored := reading(1, -30, -30)

	cases := []struct {
		name    string
		r       session.Reading
		target  int
		want    int
		wantErr string
		note    string
	}{
		{"up", reading(50, 0, 0), 120, 70, "", "within about 1 s"},
		{"down", reading(50, 0, 0), 20, -30, "", ""},
		{"to the floor", reading(50, 0, 0), 1, -49, "", ""},
		{"already there", reading(50, 0, 0), 50, 0, "", "already at the target"},
		{"below one", reading(50, 0, 0), 0, 0, "between 1 and 500", ""},
		{"negative", reading(50, 0, 0), -5, 0, "between 1 and 500", ""},
		{"above the cap", reading(50, 0, 0), 501, 0, "--max-rps", ""},
		{"at the cap but the plan peak plus bias would pass it", reading(50, 0, 0), 500, 0, "above the server limit", ""},
		{"no reading yet", session.Reading{}, 10, 0, "no telemetry reading yet", ""},
		{"stale reading", stale, 10, 0, "s old", ""},
		{"earlier change still taking effect", unsettled, 10, 0, "still taking effect", ""},
		{"floored reading hides the profile rate", floored, 40, 0, "adjust_rps with a positive delta (+30", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, note, err := planSet(l, c.target)(c.r)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want || !strings.Contains(note, c.note) {
				t.Fatalf("got %d %q %v", got, note, err)
			}
		})
	}
}

func TestCheckSteerInput_Table(t *testing.T) {
	at := 3.0
	cases := []struct {
		name string
		in   SteerInput
		err  string
	}{
		{"adjust ok", SteerInput{Action: "adjust_rps", Delta: 5}, ""},
		{"adjust zero", SteerInput{Action: "adjust_rps"}, "non-zero delta"},
		{"adjust with target", SteerInput{Action: "adjust_rps", Delta: 5, TargetRPS: 9}, "only delta"},
		{"set ok", SteerInput{Action: "set_rps", TargetRPS: 5}, ""},
		{"set with delta", SteerInput{Action: "set_rps", TargetRPS: 5, Delta: 1}, "only target_rps"},
		{"hold ok", SteerInput{Action: "hold_rps", TargetRPS: 40}, ""},
		{"hold without target", SteerInput{Action: "hold_rps"}, "needs target_rps"},
		{"hold with delta", SteerInput{Action: "hold_rps", TargetRPS: 40, Delta: 2}, "only target_rps"},
		{"release ok", SteerInput{Action: "release_hold"}, ""},
		{"release with target", SteerInput{Action: "release_hold", TargetRPS: 5}, "no other arguments"},
		{"mark ok", SteerInput{Action: "mark", Label: "x", AtElapsedS: &at}, ""},
		{"mark no label", SteerInput{Action: "mark"}, "needs a label"},
		{"mark with delta", SteerInput{Action: "mark", Label: "x", Delta: 1}, "only label"},
		{"unknown", SteerInput{Action: "pause"}, `unknown action "pause"`},
		{"missing", SteerInput{}, "unknown action"},
	}
	for _, c := range cases {
		err := checkSteerInput(c.in)
		if (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.err)
		}
	}
}
