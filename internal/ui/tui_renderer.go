package ui

import (
	"github.com/shyam-s00/gopher-glide/internal/autopilot"
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
	"github.com/shyam-s00/gopher-glide/internal/tui"
)

// TUIRenderer is the default interactive presentation layer.
// It delegates directly to the existing BubbleTea tui.Start implementation.
type TUIRenderer struct {
	// Autopilot, when set, runs the built-in controller with the run.
	Autopilot *autopilot.Params

	// biasEvents is appended to live by the TUI model as arrow-key bias happens
	// (tui.Start takes its address), because main.go's onRunComplete closure has no
	// other way to reach a running model.
	biasEvents []snap.BiasEvent

	ap tui.Autopilot
}

// Run launches the BubbleTea TUI and blocks until the user quits or all
// engine stages complete.
func (r *TUIRenderer) Run(eng engine.Runner, cfg *config.Config, specs []httpreader.RequestSpec, opts RunOptions) error {
	var ap *tui.Autopilot
	if r.Autopilot != nil {
		r.ap = tui.Autopilot{Params: *r.Autopilot}
		ap = &r.ap
	}
	return tui.Start(eng, cfg, specs, opts.Snapping, opts.SnapDir, opts.OnRunComplete, opts.TickInterval, &r.biasEvents, ap)
}

// BiasEvents returns every bias command recorded this run. Safe to read once
// Run() has returned; also safe to read live from within OnRunComplete since
// the model writes through the same pointer synchronously on each keypress.
func (r *TUIRenderer) BiasEvents() []snap.BiasEvent {
	return r.biasEvents
}

// Marks returns the annotations the autopilot dropped; the TUI has no manual marks.
func (r *TUIRenderer) Marks() []snap.Mark {
	return r.ap.Marks
}

// AutopilotMeta is the autopilot's record for the snapshot, nil when none ran.
func (r *TUIRenderer) AutopilotMeta() *snap.AutopilotMeta {
	return autopilotMeta(r.ap.Result, nil)
}
