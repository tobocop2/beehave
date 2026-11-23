package host

import (
	"fmt"

	"github.com/tobocop2/beehave/internal/model"
)

// slotRetrySeconds is the wait between two tries to take a slot.
const slotRetrySeconds = 2

// slotScript takes one of Count lock files with flock(1) before the command runs. The lock lives on file descriptor 9, which the command and its children
// inherit, so the kernel frees the slot when the last of them ends. The loop tries every lock file without waiting, and a job that finds none
// writes the marker and tries again. The arguments are the folder, the marker, the count, the class and the wait in seconds.
const slotScript = `mkdir -p %[1]s || exit 1; bh_d=%[1]s; bh_m=%[2]s; bh_w=0
until bh_i=1; while [ "$bh_i" -le %[3]d ]; do exec 9>"$bh_d/$bh_i.lock"; flock -n 9 && break; exec 9>&-; bh_i=$((bh_i+1)); done; [ "$bh_i" -le %[3]d ]; do
[ "$bh_w" = 1 ] || { echo "waiting for a slot of class %[4]s on this host"; bh_w=1; }; : > "$bh_m"; sleep %[5]d; done
rm -f "$bh_m"
`

// SlotWrap returns the job script that takes a slot of the class before it runs command. folder and marker are shell words that the caller has quoted:
// the folder that holds the lock files of the class, and the file that exists while the job waits. A job with no slot gets the command as it is.
// The command runs as a child of the script, so the lock stays held until the command and its children end.
func SlotWrap(slot model.Slot, folder, marker, command string) string {
	if !slot.Limited() {
		return command
	}

	return fmt.Sprintf(slotScript, folder, marker, slot.Count, slot.Class, slotRetrySeconds) + command
}
