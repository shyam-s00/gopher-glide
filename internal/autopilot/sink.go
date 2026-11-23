package autopilot

import "context"

// MsgKind says what a Msg carries.
type MsgKind int

const (
	MsgMark MsgKind = iota
	MsgEvent
	MsgStop
)

// Msg is one thing the controller told its Sink.
type Msg struct {
	Kind  MsgKind
	Label string // MsgMark
	Event Event  // MsgEvent
}

// ChanSink hands controller output to the goroutine that owns the run, so
// marks and events are recorded and printed from one place.
type ChanSink struct {
	C   chan Msg
	ctx context.Context
}

// NewChanSink returns a sink whose sends give up once ctx is done, so a
// controller never blocks on a host that has stopped listening.
func NewChanSink(ctx context.Context) *ChanSink {
	return &ChanSink{C: make(chan Msg, 64), ctx: ctx}
}

func (s *ChanSink) send(m Msg) {
	select {
	case s.C <- m:
	case <-s.ctx.Done():
	}
}

func (s *ChanSink) Mark(label string) { s.send(Msg{Kind: MsgMark, Label: label}) }
func (s *ChanSink) Event(e Event)     { s.send(Msg{Kind: MsgEvent, Event: e}) }
func (s *ChanSink) StopRun()          { s.send(Msg{Kind: MsgStop}) }

var _ Sink = (*ChanSink)(nil)
