// LCP 3.1 zero-cost check — see ignore/live-control-protocol.md §3.1.
//
// Run: go test -bench=BenchmarkHeadlessRenderer_Idle -benchmem ./internal/ui/
package ui

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/shyam-s00/gopher-glide/internal/config"
	"github.com/shyam-s00/gopher-glide/internal/httpreader"
)

// benchRunner keeps RunStages "in progress" for a fixed duration so the
// heartbeat ticker fires repeatedly per Run() call, then returns — enough to
// exercise the select loop's steady state, not just started/finished.
type benchRunner struct {
	fakeRunner
	dur time.Duration
}

func (r benchRunner) RunStages(ctx context.Context, cfg *config.Config, specs []httpreader.RequestSpec) error {
	select {
	case <-time.After(r.dur):
	case <-ctx.Done():
	}
	return nil
}

// blockUntil never yields data until done is closed, simulating an idle
// --control stdin connection — an EOF reader would let controlReader exit
// early and stop exercising the cmdCh select arm during the measured loop.
type blockUntil struct{ done <-chan struct{} }

func (b blockUntil) Read(p []byte) (int, error) {
	<-b.done
	return 0, io.EOF
}

// BenchmarkHeadlessRenderer_Idle is the 3.1 zero-cost check: the extra
// select arm must not slow the heartbeat loop, disabled or idle. ns/op
// should match; allocs/op differs only by the listener's one-time setup.
func BenchmarkHeadlessRenderer_Idle(b *testing.B) {
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatalf("open devnull: %v", err)
	}
	defer devNull.Close()
	orig := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = orig }()

	cfg := testConfig()
	const heartbeat = 200 * time.Microsecond
	const runFor = 20 * time.Millisecond // ~100 heartbeats/iteration

	done := make(chan struct{})
	defer close(done)

	cases := []struct {
		name        string
		controlMode string
		input       io.Reader
	}{
		{"control_none", "none", nil},
		{"control_stdin_idle", "stdin", blockUntil{done: done}},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := &HeadlessRenderer{
					Reporter:          "json",
					ControlMode:       tc.controlMode,
					ControlInput:      tc.input,
					HeartbeatInterval: heartbeat,
				}
				if err := r.Run(benchRunner{dur: runFor}, cfg, nil, RunOptions{}); err != nil {
					b.Fatalf("Run: %v", err)
				}
			}
		})
	}
}
