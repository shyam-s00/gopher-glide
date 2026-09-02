package ui

import (
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/snap"
	"github.com/shyam-s00/gopher-glide/internal/tui"
)

// TUIRenderer is the default interactive presentation layer.
// It delegates directly to the existing BubbleTea tui.Start implementation.
type TUIRenderer struct {
	// biasEvents is written into live by the TUI model as arrow-key bias
	// commands happen (tui.Start takes its address) — main.go's
	// onRunComplete closure has no other way to reach a running model, since
	// it's built and invoked before/during Start, not after it returns.
	biasEvents []snap.BiasEvent
}

// Run launches the BubbleTea TUI and blocks until the user quits or all
// engine stages complete.
func (r *TUIRenderer) Run(eng engine.Runner, cfg *config.Config, specs []httpreader.RequestSpec, opts RunOptions) error {
	return tui.Start(eng, cfg, specs, opts.Snapping, opts.SnapDir, opts.OnRunComplete, opts.TickInterval, &r.biasEvents)
}

// BiasEvents returns every bias command recorded this run. Safe to read once
// Run() has returned; also safe to read live from within OnRunComplete since
// the model writes through the same pointer synchronously on each keypress.
func (r *TUIRenderer) BiasEvents() []snap.BiasEvent {
	return r.biasEvents
}
