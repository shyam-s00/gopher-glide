package main

import (
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/config"
)

func TestAutopilotFlags_Build(t *testing.T) {
	plan := func() *config.Config {
		return &config.Config{Stages: []config.Stage{{Duration: time.Minute, TargetRPS: 40}}}
	}

	t.Run("unset is no autopilot", func(t *testing.T) {
		cfg := plan()
		p, err := (&autopilotFlags{}).build(cfg, false, 0)
		if p != nil || err != nil || len(cfg.Stages) != 1 {
			t.Fatalf("p=%v err=%v stages=%v", p, err, cfg.Stages)
		}
	})

	t.Run("find-ceiling owns the schedule", func(t *testing.T) {
		cfg := plan()
		f := &autopilotFlags{mode: "find-ceiling", maxP99: 200 * time.Millisecond, maxRPS: 300}
		p, err := f.build(cfg, false, 2*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if p.MaxP99Ms != 200 || p.MaxRPS != 300 || p.StartRPS != 20 || p.MaxDuration != 2*time.Minute {
			t.Errorf("params = %+v", p)
		}
		if len(cfg.Stages) != 1 || cfg.Stages[0].TargetRPS != 300 || cfg.Stages[0].Duration != 2*time.Minute+autopilot.FindCeilingMargin {
			t.Errorf("stages = %+v, want one flat stage at the cap that outlasts the controller", cfg.Stages)
		}
	})

	t.Run("find-ceiling rejects a profile", func(t *testing.T) {
		if _, err := (&autopilotFlags{mode: "find-ceiling"}).build(plan(), true, 0); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("slo-guard keeps the plan", func(t *testing.T) {
		cfg := plan()
		p, err := (&autopilotFlags{mode: "slo-guard"}).build(cfg, true, 0)
		if err != nil || p.Mode != autopilot.ModeSLOGuard || len(cfg.Stages) != 1 || cfg.Stages[0].TargetRPS != 40 {
			t.Fatalf("p=%+v err=%v stages=%v", p, err, cfg.Stages)
		}
	})

	t.Run("bad values are errors", func(t *testing.T) {
		for _, f := range []autopilotFlags{{mode: "chaos"}, {mode: "slo-guard", maxErrorRate: 3}, {mode: "find-ceiling", startRPS: 90, maxRPS: 10}} {
			if _, err := f.build(plan(), false, 0); err == nil {
				t.Errorf("%+v accepted", f)
			}
		}
	})
}
