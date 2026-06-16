package hive

import (
	"net/http"
	"time"
)

const (
	// minPoolPerHost is the floor for MaxIdleConnsPerHost regardless of RPS.
	minPoolPerHost = 100

	// poolHostMultiplier scales MaxIdleConns relative to MaxIdleConnsPerHost
	// to accommodate tests that hit multiple hosts (e.g. redirect targets).
	poolHostMultiplier = 4

	// fdReserve is the number of file descriptors withheld from the pool
	// budget for stdio, log files, and other OS/runtime overhead.
	fdReserve = 256

	// aggressiveIdleTimeout releases idle sockets quickly so that the pool
	// shrinks naturally when the engine scales down between stages.
	aggressiveIdleTimeout = 10 * time.Second
)

// buildTransport returns an *http.Transport whose idle-connection pool is
// sized proportionally to peakRPS and capped by the host OS file-descriptor
// limit reported by RLIMIT_NOFILE.
//
// Sizing formula:
//
//	MaxIdleConnsPerHost = max(peakRPS/10, minPoolPerHost)
//	MaxIdleConns        = MaxIdleConnsPerHost × poolHostMultiplier
//
// Both values are then clamped to the available FD budget so the engine
// never exhausts the host's open-file limit under high concurrency.
//
// Rationale for peakRPS/10:
//
//	With a median HTTP latency of ~10 ms and Little's Law (L = λW), the
//	expected steady-state concurrency is roughly peakRPS × 0.01 s = peakRPS/100.
//	The /10 divisor adds a ×10 safety margin to absorb latency spikes and
//	bursty dispatch without forcing TCP connection churn.
func buildTransport(peakRPS int) *http.Transport {
	// fdBudget() is implemented per-platform (pool_unix.go / pool_windows.go);
	// returns 0 only when the limit cannot be queried — caller treats as
	// uncapped. A known limit (however small) always yields budget > 0, so a
	// tight ulimit always results in the pool being clamped down below.
	return buildTransportWithBudget(peakRPS, fdBudget())
}

// buildTransportWithBudget is the budget-parameterized core of
// buildTransport, split out so tests can exercise the FD-clamping logic
// without depending on the host's actual RLIMIT_NOFILE.
func buildTransportWithBudget(peakRPS, budget int) *http.Transport {
	perHost := peakRPS / 10
	if perHost < minPoolPerHost {
		perHost = minPoolPerHost
	}
	total := perHost * poolHostMultiplier

	// Clamp to the OS file-descriptor budget when it can be determined.
	if budget > 0 {
		if total > budget {
			total = budget
		}
		half := budget / 2
		if perHost > half {
			perHost = half
		}
		// Ensure perHost <= total after clamping.
		if perHost > total {
			perHost = total
		}
	}

	return &http.Transport{
		MaxIdleConns:        total,
		MaxIdleConnsPerHost: perHost,
		// Aggressive timeout: idle sockets released within 10 s of becoming
		// idle, keeping pool memory low during ramp-down phases.
		IdleConnTimeout: aggressiveIdleTimeout,
		// Keep-alives on — connection reuse is essential for high-RPS loads.
		DisableKeepAlives: false,
	}
}
