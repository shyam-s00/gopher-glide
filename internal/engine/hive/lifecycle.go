package hive

import "time"

// IsRunning returns true while RunStages is executing.
// It becomes true at the very start of RunStages and false in its defer,
// so it is safe to poll from any goroutine.
func (e *Engine) IsRunning() bool {
	return e.isRunning.Load()
}

// GetStartTime returns the wall-clock time at which RunStages began.
// Returns a zero time if the engine has not been started yet.
func (e *Engine) GetStartTime() time.Time {
	e.timeMu.RLock()
	defer e.timeMu.RUnlock()
	return e.startTime
}

// GetEndTime returns the wall-clock time at which RunStages completed.
// Returns time.Now() if the engine is still running or has not started yet,
// so callers always receive a usable wall-clock value.
func (e *Engine) GetEndTime() time.Time {
	e.timeMu.RLock()
	t := e.endTime
	e.timeMu.RUnlock()
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// GetElapsedTime returns the number of seconds since RunStages started.
//
// Behaviour by state:
//   - Before start : returns 0
//   - During run   : returns live elapsed seconds (time.Since(startTime))
//   - After end    : returns the fixed final duration (endTime − startTime)
func (e *Engine) GetElapsedTime() float64 {
	e.timeMu.RLock()
	start := e.startTime
	end := e.endTime
	e.timeMu.RUnlock()

	if start.IsZero() {
		return 0
	}
	if end.IsZero() {
		return time.Since(start).Seconds()
	}
	return end.Sub(start).Seconds()
}
