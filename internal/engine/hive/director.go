package hive

// ApplyBias sends a cumulative RPS delta to the Queen via the buffered biasCh.
//
// The send is non-blocking: if the channel buffer (capacity 16) is full the
// delta is silently dropped. In practice the Queen drains biasCh on every
// 1-second heartbeat tick, so the buffer never fills under normal TUI usage.
// Negative deltas are accepted and reduce the effective RPS.
func (e *Engine) ApplyBias(delta int) {
	select {
	case e.biasCh <- delta:
	default: // drop silently — buffer full
	}
}

// GetBias returns the current cumulative manual RPS bias as set by prior
// ApplyBias calls and drained by the Queen. A positive value means the live
// RPS has been nudged up; negative means it has been nudged down.
func (e *Engine) GetBias() int {
	return int(e.rpsBias.Load())
}

// SetTargetRPS hard-sets the target RPS atomic that the Queen publishes into
// MetricsSnapshot.TargetRPS. Intended for Director Mode overrides where the
// caller wants to bypass stage LERP and jump directly to a specific rate.
func (e *Engine) SetTargetRPS(rps int) {
	e.targetRPS.Store(int64(rps))
}
