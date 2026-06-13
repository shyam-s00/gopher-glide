//go:build !windows

package hive

import "syscall"

// minFDBudget is the smallest non-zero budget fdBudgetFromLimit will report
// when RLIMIT_NOFILE is known but too small to satisfy fdReserve.
//
// It matters because buildTransport treats a budget of exactly 0 as
// "unknown — don't clamp", and because http.Transport itself treats 0
// specially (MaxIdleConns=0 means unlimited; MaxIdleConnsPerHost=0 falls
// back to DefaultMaxIdleConnsPerHost). A tiny-but-known ulimit must still
// produce a small positive cap, never 0.
const minFDBudget = 8

// fdBudget returns the number of file descriptors available to the process
// after reserving fdReserve slots for non-connection overhead.
//
// Uses RLIMIT_NOFILE to read the soft limit set by the OS or the user's
// shell (ulimit -n). Returns 0 only if the limit cannot be queried; the
// caller (buildTransport) treats 0 as "uncapped". A successfully-queried
// limit always yields a positive budget — see fdBudgetFromLimit.
func fdBudget() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0 // unknown — caller treats as uncapped
	}
	return fdBudgetFromLimit(int(rl.Cur))
}

// fdBudgetFromLimit computes the connection-pool FD budget for a known
// RLIMIT_NOFILE soft limit (cur).
//
// cur - fdReserve is the common case. When the limit is small enough that
// fdReserve would consume it entirely (or more), withholding the full
// fdReserve no longer makes sense — the limit is the binding constraint, not
// our usual overhead estimate. In that case we fall back to a small positive
// floor (minFDBudget). This keeps buildTransport's fd-cap clamp active for
// tight ulimits instead of silently treating "limit known and tiny" the same
// as "limit unknown" (which buildTransport would otherwise read as
// "uncapped"). Degenerate limits below minFDBudget (e.g. ulimit -n 0, which
// a real process couldn't run under anyway) intentionally still yield
// minFDBudget rather than 0.
func fdBudgetFromLimit(cur int) int {
	available := cur - fdReserve
	if available < minFDBudget {
		available = minFDBudget
	}
	return available
}
