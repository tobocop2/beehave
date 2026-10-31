package proc

import "os"

// userProcesses returns the tasks (threads) that the user of this process runs, read from /proc the way the kernel counts them.
func userProcesses() (int, error) {
	return countTasks("/proc", os.Getuid())
}
