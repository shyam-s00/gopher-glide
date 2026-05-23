package hive

import (
	"net/http"
	"syscall"
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
	perHost := peakRPS / 10
	if perHost < minPoolPerHost {
		perHost = minPoolPerHost
	}
	total := perHost * poolHostMultiplier

	// Clamp to OS file-descriptor budget when it can be determined.
	if budget := fdBudget(); budget > 0 {
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

// fdBudget returns the number of file descriptors available to the process
// after reserving fdReserve slots for non-connection overhead.
//
// Returns 0 if the OS limit cannot be queried (caller treats as uncapped).
func fdBudget() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		// Limit unreadable — leave pool uncapped rather than crash.
		return 0
	}
	cur := int(rl.Cur)
	available := cur - fdReserve
	if available <= 0 {
		return 0
	}
	return available
}
