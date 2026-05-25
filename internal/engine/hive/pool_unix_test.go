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
