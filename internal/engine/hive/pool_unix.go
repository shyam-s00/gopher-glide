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

// fdBudget returns the FDs available to the process after reserving fdReserve for
// non-connection overhead, from the RLIMIT_NOFILE soft limit. 0 means the limit
// couldn't be queried (treated as uncapped).
func fdBudget() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0 // unknown — caller treats as uncapped
	}
	return fdBudgetFromLimit(int(rl.Cur))
}

// fdBudgetFromLimit turns a known RLIMIT_NOFILE soft limit into a pool budget:
// cur - fdReserve, floored at minFDBudget so a tiny ulimit still clamps the pool
// instead of being read as "unknown" (0).
func fdBudgetFromLimit(cur int) int {
	available := cur - fdReserve
	if available < minFDBudget {
		available = minFDBudget
	}
	return available
}
