// Package seatbelt is the sandbox enforcer for macOS. It runs the command under sandbox-exec with a Seatbelt profile that obee generates from the plan.
//
// The profile denies everything by default and then allows what the plan lists: reads by path, writes by path (a write path is readable too), and TCP
// connect to the port of the allow-list proxy on localhost. It grants no unix socket, so a command reaches no socket outside the plan, a pathname socket
// or any other. Seatbelt lets a program stat any path (file-read-metadata), so a command can learn that a file exists but cannot read it.
//
// The text of the profile holds no path. Each path of the plan is passed to sandbox-exec as a -D parameter that the profile reads with (param "name"),
// so a path with a quote, a parenthesis or a newline cannot add a rule.
//
// sandbox-exec is deprecated by Apple, and it is still part of every macOS. If the program is missing, or the system refuses the profile, Check says that the
// enforcer cannot run, and a gate under the sandbox is refused. It is never run unconfined.
package seatbelt

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/sandbox"
)

// Name is the name of this enforcer in sandbox_enforcer.
const Name = "seatbelt"

// header is the part of the profile that does not depend on the plan: deny by default, then what every program needs to start and to run.
const header = `(version 1)
(deny default)
(allow process-exec)
(allow process-fork)
(allow signal (target self))
(allow sysctl-read)
(allow mach-lookup
	(global-name "com.apple.system.opendirectoryd.libinfo")
	(global-name "com.apple.system.logger")
	(global-name "com.apple.logd")
	(global-name "com.apple.system.notification_center")
	(global-name "com.apple.cfprefsd.daemon")
	(global-name "com.apple.diagnosticd"))
(allow ipc-posix-shm-read-data)
(allow file-read-metadata)
(allow file-ioctl)
(allow file-read-data (literal "/"))
`

// systemRead lists the folders that a program on macOS needs to start, on top of sandbox.SystemRead: the system frameworks and the real folder of /etc.
func systemRead() []string {
	return []string{"/System", "/Library/Apple", "/private/etc", "/private/var/db/dyld", "/private/var/db/timezone", "/private/var/select"}
}

// Profile returns the Seatbelt profile for a plan and the -D arguments that give the profile its paths and its port. port is the port of the allow-list
// proxy, or 0 when the plan has no network. The text depends on the number of paths only.
func Profile(plan sandbox.Plan, port int) (text string, defines []string) {
	var b strings.Builder

	b.WriteString(header)

	reads := append(append(sandbox.SystemRead(), systemRead()...), plan.Read...)
	defines = rules(&b, "R", "file-read*", reads, defines)
	defines = rules(&b, "W", "file-read* file-write*", plan.Write, defines)

	if port > 0 {
		b.WriteString("(allow network-outbound (remote ip (string-append \"localhost:\" (param \"PORT\"))))\n")

		defines = append(defines, "PORT="+strconv.Itoa(port))
	}

	return b.String(), defines
}

// rules writes one allow rule for each path and adds its -D argument. A folder is allowed with all that lies below it, a file by itself.
func rules(b *strings.Builder, prefix, operations string, paths, defines []string) []string {
	for i, path := range paths {
		name := prefix + strconv.Itoa(i)
		fmt.Fprintf(b, "(allow %s (%s (param %q)))\n", operations, filter(path), name)

		defines = append(defines, name+"="+resolve(path))
	}

	return defines
}

// filter returns the Seatbelt path filter for a path: subpath for a folder or a path that does not exist yet, literal for a file.
func filter(path string) string {
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		return "literal"
	}

	return "subpath"
}

// resolve follows the symbolic links of a path, because Seatbelt compares the real path (/tmp is /private/tmp). A path that does not exist is kept as it is.
func resolve(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}

	return resolved
}
