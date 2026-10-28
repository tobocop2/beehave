package config

import (
	"fmt"
	"os/exec"
	"strings"
)

// BoxSSHProblem returns one line when BOX_SSH is set but its first word is not a command this
// host can run, and "" otherwise. A stale prefix fails every heartbeat touch, and the idle
// watchdog of the build host then powers the live host off.
func BoxSSHProblem(boxSSH string) string {
	if boxSSH == "" {
		return ""
	}

	first := ""
	if words := strings.FieldsFunc(boxSSH, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }); len(words) > 0 {
		first = words[0]
	}

	if _, err := exec.LookPath(first); err == nil {
		return ""
	}

	return fmt.Sprintf("BOX_SSH NOT RUNNABLE: round.env sets BOX_SSH to '%s', and '%s' does not exist. "+
		"Point BOX_SSH at the live build host's helper, or remove it if no host is up.", boxSSH, first)
}
