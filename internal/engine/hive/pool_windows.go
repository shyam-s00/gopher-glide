//go:build windows

package hive

// fdBudget on Windows always returns 0 (uncapped) because Windows does not
// expose a per-process file-descriptor limit via the POSIX RLIMIT_NOFILE
// interface. buildTransport will use the formula-derived pool sizes without
// an OS-level clamp, which is safe on Windows where the handle limit is
// managed by the kernel and is typically in the tens of thousands.
func fdBudget() int {
	return 0
}
