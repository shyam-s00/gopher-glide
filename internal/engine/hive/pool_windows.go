//go:build windows

package hive

// fdBudget on Windows returns 0 (uncapped): there is no per-process RLIMIT_NOFILE
// equivalent, and the kernel manages the handle limit.
func fdBudget() int {
	return 0
}
