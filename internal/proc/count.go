package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// countTasks counts the tasks (threads) of the processes that uid owns in procDir, a folder laid out like /proc on Linux.
// The Linux kernel counts tasks against the process limit of a user, so a count of processes only would be too low.
// A process that ends while the count runs, or a name that is not a process, adds nothing.
func countTasks(procDir string, uid int) (int, error) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return 0, err
	}

	total := 0

	for _, entry := range entries {
		if _, numeric := strconv.Atoi(entry.Name()); numeric != nil {
			continue
		}

		total += tasksOf(filepath.Join(procDir, entry.Name()), uid)
	}

	return total, nil
}

// tasksOf counts the tasks of the process in dir, or zero when uid does not own it or it cannot be read.
func tasksOf(dir string, uid int) int {
	info, err := os.Stat(dir)
	if err != nil || !ownedBy(info, uid) {
		return 0
	}

	tasks, err := os.ReadDir(filepath.Join(dir, "task"))
	if err != nil {
		return 0
	}

	return len(tasks)
}

// ownedBy reports whether uid owns the file that info describes.
func ownedBy(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)

	return ok && int(stat.Uid) == uid
}
