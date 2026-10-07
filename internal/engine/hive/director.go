package hive

// ApplyBias adds a signed RPS delta to the cumulative bias. The new value is
// visible to GetBias immediately; the Queen folds it into the target rate at
// the start of its next 1-second window. Never blocks or drops a delta.
func (e *Engine) ApplyBias(delta int) {
	e.rpsBias.Add(int64(delta))
}

// GetBias returns the current cumulative manual RPS bias from all prior
// ApplyBias calls. A positive value means the live
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
