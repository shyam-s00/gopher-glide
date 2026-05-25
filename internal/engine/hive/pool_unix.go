//go:build !windows

package hive

import "syscall"

// fdBudget returns the number of file descriptors available to the process
// after reserving fdReserve slots for non-connection overhead.
//
// Uses RLIMIT_NOFILE to read the soft limit set by the OS or the user's
// shell (ulimit -n). Returns 0 if the limit cannot be queried; the caller
// (buildTransport) treats 0 as "uncapped".
func fdBudget() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0
	}
	cur := int(rl.Cur)
	available := cur - fdReserve
	if available <= 0 {
		return 0
	}
	return available
}
