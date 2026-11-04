package shell

import (
	"path"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	cmdRsync = "rsync"
	cmdScp   = "scp"
	cmdCloud = "gcloud"
	// cmdKubectl runs a command in a pod or copies a file to one.
	cmdKubectl = "kubectl"
	nullDevice = "/dev/null"
	// copyHint is how a file reaches another machine: the host reads the code from the forge.
	copyHint = "a repository that is not private sends no file from this machine to another machine. Clone the repository from the forge on the host instead"
)

// scanLaptopCopy adds a finding for a command that copies from this machine to another one: rsync, scp or kubectl cp with a remote target, or a file or a pipe into a remote shell.
// The kind blocks the common forms. It does not block every sender: the row S1-H-45 of the conformance table names what it misses.
// A command that runs on a host (remote) copies from that host, not from this machine, so it passes. A private repository may copy.
// A heredoc or a here-string into a remote shell passes: it sends text the agent wrote, not a file of this machine.
// A command substitution inside that text can still pull in a file; the kind does not look for one.
func (s *scanner) scanLaptopCopy(cmd *command, remote bool) {
	if s.private || remote || len(cmd.head) == 0 {
		return
	}

	operands, copies, toRemote := copyOperands(cmd.head)

	switch {
	case (cmd.prev != nil || readsFile(cmd.reads)) && remoteShell(cmd.head, s.funcs):
		s.add(model.FindingLaptopCopy, []string{"a pipe or a file into", cmd.head[0] + ":", copyHint})
	case copies && sendsToRemote(operands, toRemote):
		s.add(model.FindingLaptopCopy, []string{cmd.head[0], "copies to another machine:", copyHint})
	}
}

// readsFile reports whether a redirect reads a file of this machine as standard input. The null device holds no data.
func readsFile(files []string) bool {
	return slices.ContainsFunc(files, func(file string) bool { return file != nullDevice })
}

// remoteShell reports whether the command opens a shell or runs a command on another machine and sends its standard input there:
// ssh, gcloud compute ssh, a ssh wrapper, az ssh, aws ssm start-session, or kubectl exec with standard input open.
// docker exec is not in the list: its daemon is local unless the environment says otherwise.
func remoteShell(head []string, funcs map[string]bool) bool {
	if _, remote := remoteOf(head, funcs); remote {
		return true
	}

	words := operandWords(beforeDashDash(head[1:]))

	switch path.Base(head[0]) {
	case "az":
		return len(words) > 0 && words[0] == SSHName
	case "aws":
		at := slices.Index(words, "ssm")

		return at >= 0 && at+1 < len(words) && words[at+1] == "start-session"
	case cmdKubectl:
		return slices.Contains(words, "exec") && slices.ContainsFunc(beforeDashDash(head[1:]), keepsStdin)
	}

	return false
}

// beforeDashDash returns the words before `--`.
func beforeDashDash(words []string) []string {
	if at := slices.Index(words, dashDash); at >= 0 {
		return words[:at]
	}

	return words
}

// keepsStdin reports whether a kubectl option word keeps standard input open: -i, a group such as -it, or --stdin.
func keepsStdin(word string) bool {
	if strings.HasPrefix(word, "--") {
		return word == "--stdin" || strings.HasPrefix(word, "--stdin=")
	}

	return strings.HasPrefix(word, "-") && strings.ContainsRune(word, 'i')
}

// copyOperands returns the operands of an rsync, scp, gcloud compute scp or kubectl cp command, whether the command is one of them,
// and the test that says whether an operand names another machine. A word after `--` is an operand.
func copyOperands(head []string) ([]string, bool, func(string) bool) {
	switch path.Base(head[0]) {
	case cmdRsync, cmdScp:
		return operandWords(head[1:]), true, isRemoteOperand
	case cmdCloud:
		positionals := gcloudPositionals(head[1:])
		at := slices.Index(positionals, cmdScp)

		return positionals[min(at+1, len(positionals)):], at >= 0, isRemoteOperand
	case cmdKubectl:
		words := operandWords(head[1:])
		at := slices.Index(words, "cp")

		return words[min(at+1, len(words)):], at >= 0, isPodOperand
	}

	return nil, false, nil
}

// isPodOperand reports whether a kubectl cp operand is `pod:path` or `namespace/pod:path`. kubectl reads every colon as a pod, except in a path that starts with `/` or `.`.
func isPodOperand(operand string) bool {
	return strings.IndexByte(operand, ':') > 0 && !strings.HasPrefix(operand, "/") && !strings.HasPrefix(operand, ".")
}

// operandWords returns the words that are not options. A word after `--` is not an option.
func operandWords(words []string) []string {
	var out []string

	ended := false

	for _, word := range words {
		switch {
		case !ended && word == dashDash:
			ended = true
		case ended || !strings.HasPrefix(word, "-"):
			out = append(out, word)
		}
	}

	return out
}

// sendsToRemote reports whether the last operand names another machine while an earlier operand is a path on this one.
func sendsToRemote(operands []string, remote func(string) bool) bool {
	if len(operands) < 2 || !remote(operands[len(operands)-1]) {
		return false
	}

	return slices.ContainsFunc(operands[:len(operands)-1], func(source string) bool { return !remote(source) })
}

// isRemoteOperand reports whether the operand is `host:path`, `user@host:path` or an rsync, ssh or scp address. A path with a slash before its first colon is local.
func isRemoteOperand(operand string) bool {
	if strings.HasPrefix(operand, "rsync://") || strings.HasPrefix(operand, "ssh://") || strings.HasPrefix(operand, "scp://") {
		return true
	}

	colon := strings.IndexByte(operand, ':')

	return colon > 0 && !strings.Contains(operand[:colon], "/")
}
