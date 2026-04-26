package ui

import (
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
	"github.com/shyam-s00/gopher-glide/internal/tui"
)

// TUIRenderer is the default interactive presentation layer.
// It delegates directly to the existing BubbleTea tui.Start implementation.
type TUIRenderer struct{}

// Run launches the BubbleTea TUI and blocks until the user quits or all
// engine stages complete.
func (r *TUIRenderer) Run(eng *engine.Engine, cfg *config.Config, specs []httpreader.RequestSpec, opts RunOptions) error {
	return tui.Start(eng, cfg, specs, opts.Snapping, opts.SnapDir, opts.OnRunComplete)
}
