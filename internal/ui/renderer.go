// Package ui provides the Renderer abstraction that decouples application
// logic from the presentation layer. Two implementations are provided:
//
//   - TUIRenderer – the default interactive BubbleTea terminal UI
//   - HeadlessRenderer – a non-interactive executor suitable for CI pipelines
//
// Choose the implementation via New(headless bool).
package ui

import (
	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/engine"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// RunOptions carries options that apply to both renderer implementations.
type RunOptions struct {
	// Snapping is true when --snap is active. Used by the TUI to display the
	// 📸 indicator and by the headless renderer to emit snap progress lines.
	Snapping bool

	// SnapDir is the resolved snapshot directory (shown in the indicator).
	SnapDir string

	// OnRunComplete is called exactly once after the engine finishes all stages
	// (or is interrupted). The returned string is surfaced to the user after
	// the run ends. nil is safe (no-op).
	//
	// Calling convention differs by renderer:
	//   - TUIRenderer: invoked in a background goroutine while the alt-screen
	//     is still active. Must not write to stdout/stderr directly; use the
	//     returned string to surface status through the director bar instead.
	//   - HeadlessRenderer: invoked synchronously after the engine goroutine
	//     has fully exited and before Run returns. Writing to stdout/stderr is
	//     safe here because there is no alt-screen constraint.
	OnRunComplete func() string
}

// Renderer is the common interface for all presentation modes.
// Implementations are responsible for running the engine and reporting
// progress until the run finishes (or the user aborts).
type Renderer interface {
	Run(eng *engine.Engine, cfg *config.Config, specs []httpreader.RequestSpec, opts RunOptions) error
}

// New returns the appropriate Renderer for the execution context.
//
//   - headless == false → TUIRenderer (interactive BubbleTea UI)
//   - headless == true → HeadlessRenderer (plain structured log output)
func New(headless bool) Renderer {
	if headless {
		return &HeadlessRenderer{}
	}
	return &TUIRenderer{}
}
