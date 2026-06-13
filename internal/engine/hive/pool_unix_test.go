//go:build !windows

package hive

import (
	"syscall"
	"testing"
)

func TestFdBudget_NeverExceedsRlimit(t *testing.T) {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		t.Skip("Getrlimit not available:", err)
	}
	budget := fdBudget()
	if budget < 0 {
		t.Fatalf("fdBudget returned negative value %d", budget)
	}
	// budget must be ≤ Cur (we only subtract from it, never add).
	if budget > int(rl.Cur) {
		t.Errorf("fdBudget()=%d exceeds RLIMIT_NOFILE.Cur=%d", budget, rl.Cur)
	}
}

func TestFdBudgetFromLimit_KnownLimitNeverZero(t *testing.T) {
	// A successfully-queried RLIMIT_NOFILE must never produce a budget of 0,
	// regardless of how small it is — buildTransport treats 0 as "unknown,
	// don't clamp", and http.Transport treats 0 as "unlimited"/"default".
	// A known limit must always result in an active, positive clamp.
	for _, cur := range []int{0, 1, 4, 8, 16, 255, 256, 257, 1000, 65536} {
		budget := fdBudgetFromLimit(cur)
		if budget <= 0 {
			t.Errorf("fdBudgetFromLimit(%d)=%d, want > 0", cur, budget)
		}
		if budget > cur-fdReserve && cur-fdReserve >= minFDBudget {
			t.Errorf("fdBudgetFromLimit(%d)=%d, want cur-fdReserve=%d", cur, budget, cur-fdReserve)
		}
	}
}

func TestFdBudgetFromLimit_NormalCase(t *testing.T) {
	// Well above fdReserve: budget == cur - fdReserve.
	got := fdBudgetFromLimit(10_000)
	want := 10_000 - fdReserve
	if got != want {
		t.Errorf("fdBudgetFromLimit(10000)=%d, want %d", got, want)
	}
}

func TestFdBudgetFromLimit_TightUlimitClampsPool(t *testing.T) {
	// This is the scenario that motivated the fix: ulimit -n 256 with
	// fdReserve == 256 used to produce available == 0, which buildTransport
	// read as "uncapped" and sized the pool for 30k RPS regardless.
	budget := fdBudgetFromLimit(256)
	if budget <= 0 {
		t.Fatalf("fdBudgetFromLimit(256)=%d, want > 0 so buildTransport clamps", budget)
	}

	tr := buildTransportWithBudget(30_000, budget)
	if tr.MaxIdleConns > budget {
		t.Errorf("MaxIdleConns=%d exceeds budget=%d", tr.MaxIdleConns, budget)
	}
	if tr.MaxIdleConnsPerHost > budget/2 {
		t.Errorf("MaxIdleConnsPerHost=%d exceeds budget/2=%d", tr.MaxIdleConnsPerHost, budget/2)
	}
}
