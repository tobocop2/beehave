#!/bin/sh
# Make a linux amd64 machine ready as a beehest build host for the ssh provider, then prove it. See docs/design/hosts.md.
# It runs in two stages. A second run of either stage changes nothing. Neither stage installs a watchdog: each refuses to start and to
# finish unless the named self-delete watchdog unit is active, enabled and Restart=always.
#
# The root stage runs once as root, from a clone that only root and the admin user can write. It installs the apt packages, Go and uv, installs
# the metadata guard unit, makes the job user beehest-jobs with useradd, and puts the public key of the controller in the authorized_keys file
# of that user. The job user has no sudoers entry and no group that gives root. The guard rejects every user except root on every TCP port
# but 53 to the instance metadata server. The stage fails unless root still gets the token. The last line of a pass is HOST-SETUP-ROOT-DONE.
#
# The job stage runs as the job user, from a clone of the repository at the commit under test, and uses no sudo. It refuses to run as a user
# that has sudo without a password or a group that gives root. It installs the project tools, proves them in a user unit, and fails unless the
# job user gets no answer from the metadata server on ports 80 and 443 over IPv4 and IPv6. The last line of a pass is HOST-SETUP-DONE.
#
# It touches no heartbeat. The root stage deletes nothing outside /usr/local/go, /usr/local/go.new, /usr/local/go.old and its own temporary folder.
# The job stage refuses a work root that is not its canonical path, a system folder, and an existing work root that the job user does not own.
# It makes the last folder of a missing work root only, as the job user, in a folder that the job user owns. It never changes the owner or the mode of a folder.
#
# Usage: sudo sh scripts/host-setup.sh --stage root --watchdog <systemd unit> --job-key <public key file>
#        sh scripts/host-setup.sh --stage job --work-root <absolute path> --watchdog <systemd unit>
# HOST_SETUP_SYSROOT is a folder that stands for / in the tests of the script, and the top of the walk of the root stage. It is empty on a host.
set -eu

usage='usage: sudo sh scripts/host-setup.sh --stage root --watchdog <systemd unit> --job-key <public key file>  |  sh scripts/host-setup.sh --stage job --work-root <absolute path> --watchdog <systemd unit>'
min_systemd=240
landlock_major=6
landlock_minor=7
job_user=beehest-jobs
job_home=/home/$job_user
# The groups that give root, or the files of root, to a member.
root_groups='root sudo admin wheel google-sudoers docker lxd disk shadow staff'

root=$(cd "$(dirname "$0")/.." && pwd)
sysroot=${HOST_SETUP_SYSROOT:-}
go_root=$sysroot/usr/local/go
lsm_file=$sysroot/sys/kernel/security/lsm
env_file=$HOME/.config/environment.d/zz-beehest.conf
guard_unit=metadata-guard.service
guard_rules=$sysroot/etc/beehest/metadata-guard.nft
guard_unit_file=$sysroot/etc/systemd/system/$guard_unit
key_file=$sysroot$job_home/.ssh/authorized_keys
job_key_copy=$sysroot/etc/beehest/job-key.pub
stage=
work_root=
watchdog=
job_key=

fail() {
	printf 'host-setup: %s\n' "$*" >&2
	exit 1
}

say() { printf 'host-setup: %s\n' "$*"; }

read_args() {
	while [ $# -gt 0 ]; do
		[ $# -ge 2 ] || fail "$1 needs a value. $usage"
		case $1 in
		--stage) stage=$2 ;;
		--work-root) work_root=$2 ;;
		--watchdog) watchdog=$2 ;;
		--job-key) job_key=$2 ;;
		*) fail "unknown argument $1. $usage" ;;
		esac
		shift 2
	done

	case $stage in
	root) [ -z "$work_root" ] || fail "--work-root is an argument of the job stage. $usage" ;;
	job) [ -z "$job_key" ] || fail "--job-key is an argument of the root stage. $usage" ;;
	'') fail "--stage is required. $usage" ;;
	*) fail "the stage $stage is not root or job. $usage" ;;
	esac
}

# check_work_root refuses a path that the adapter refuses: systemd expands a percent sign, and a dollar sign is no path.
# It also refuses a single quote. The by-hand command of check_work_root_folder puts the path in single quotes, and a control
# character includes a newline, so the printed command runs nothing but the mkdir and the chown when the owner pastes it.
check_work_root() {
	case $work_root in
	'') fail "--work-root is required. $usage" ;;
	*[%\$]*) fail "the work root $work_root must not hold a percent or dollar sign, which systemd and the shell would expand" ;;
	*[[:cntrl:]]*) fail 'the work root must hold no control character' ;;
	*\'*) fail "the work root $work_root must not hold a single quote, which the command that the script prints for the owner of the parent cannot carry" ;;
	/*) ;;
	*) fail "the work root $work_root must be an absolute path" ;;
	esac
	check_work_root_folder
}

is_system_folder() {
	case $1 in
	/ | /bin | /boot | /dev | /etc | /home | /lib | /lib32 | /lib64 | /libx32 | /media | /mnt | /opt | /proc | /root | /run | /sbin | /snap | /srv | /sys | /tmp | /usr | /var) return 0 ;;
	esac
	return 1
}

# check_work_root_folder refuses a work root that is not its canonical path, a system folder, and an existing work root that is not a
# folder of the login user. A canonical path has no . or .. part, no doubled or trailing slash and no link, so the path names exactly
# the folder that the script reads or makes. A missing work root needs a parent folder that exists and that the login user owns.
check_work_root_folder() {
	canonical=$(realpath -m -- "$work_root") || fail "the work root $work_root cannot be read"
	[ "$canonical" = "$work_root" ] ||
		fail "the work root $work_root is not a canonical path: it names $canonical. Name the folder with no . or .. part, no doubled or trailing slash and no link"
	if is_system_folder "$work_root"; then
		fail "the work root $work_root is a system folder. Name a folder of its own, such as /home/<user>/jobs"
	fi

	if [ -e "$work_root" ] || [ -L "$work_root" ]; then
		[ -d "$work_root" ] || fail "the work root $work_root is not a folder"
		[ "$(stat -c %u -- "$work_root")" = "$(id -u)" ] ||
			fail "the work root $work_root is not owned by the login user. Name a folder of the login user, or a folder that does not exist yet"
		[ -w "$work_root" ] || fail "the login user cannot write to the work root $work_root"
		return 0
	fi

	parent=$(dirname -- "$work_root")
	[ -d "$parent" ] ||
		fail "the work root $work_root is missing, and its parent $parent is not a folder. Make the parent folder first, or name a work root whose parent exists"
	[ "$(stat -c %u -- "$parent")" = "$(id -u)" ] ||
		fail "the parent $parent of the work root $work_root is not owned by the login user, so the script does not make the work root. The owner of $parent makes it by hand: sudo mkdir -m 0755 -- '$work_root' && sudo chown $(id -un):$(id -gn) -- '$work_root'"
}

check_watchdog_name() {
	case $watchdog in
	'') fail "--watchdog is required: name the systemd unit of the self-delete watchdog. $usage" ;;
	-*) fail "the watchdog $watchdog is not a systemd unit name: it starts with a dash" ;;
	*[!A-Za-z0-9:_.@-]*) fail "the watchdog $watchdog is not a systemd unit name" ;;
	esac
}

# check_platform stops on a machine that is not linux amd64, and on a folder that is not a clone of the repository.
check_platform() {
	if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
		fail 'a build host is linux amd64 only'
	fi
	if [ ! -f "$root/go.mod" ] || [ ! -f "$root/Makefile" ]; then
		fail "$root holds no go.mod and Makefile. Run this from a clone of the repository"
	fi
}

# reject_root_groups <label> <user> <groups>: fail when one of the groups gives root. The label names the user in the message.
reject_root_groups() {
	for group in $3; do
		case " $root_groups " in
		*" $group "*) fail "$1 is in the group $group, which gives root. Remove it: sudo gpasswd -d $2 $group" ;;
		esac
	done
}

# check_no_way_to_root fails when the user of the job stage can become root: with sudo without a password, or through a group.
# A step of a gate that runs as such a user can take the token of the machine. sudo -n -l succeeds for any entry without a password,
# also one that allows some commands only.
check_no_way_to_root() {
	name=$(id -un)
	if sudo -n -l >/dev/null 2>&1; then
		fail "the user $name can run sudo without a password, so a step of a gate can become root and take the token. Run the job stage as the job user $job_user"
	fi
	reject_root_groups "the user $name" "$name" "$(id -nG)"
}

# refuse_root stops the root stage before any change.
refuse_root() {
	[ "$(id -u)" = 0 ] || fail "run the root stage as root: sudo sh scripts/host-setup.sh --stage root. $usage"
	check_platform
	check_root_paths
	check_watchdog_name
	check_job_key
}

# refuse_job stops the job stage before any change, and before any code of the clone runs.
refuse_job() {
	[ "$(id -u)" != 0 ] || fail "run the job stage as the job user $job_user, not as root"
	check_platform
	check_no_way_to_root
	check_work_root
	check_watchdog_name
}

# admin_private_group prints the primary group of the admin user (SUDO_GID) when no user but root and the admin is in it, else nothing.
# A failed lookup prints nothing, so the group then counts as a group of other users.
admin_private_group() {
	gid=${SUDO_GID:-}
	[ -n "$gid" ] || return 0
	entry=$(getent group "$gid") || return 0
	for member in $(printf '%s\n' "${entry##*:}" | tr , ' '); do
		[ "$member" = root ] || [ "$member" = "${SUDO_USER:-}" ] || return 0
	done
	users=$(getent passwd) || return 0
	others=$(printf '%s\n' "$users" | awk -F : -v gid="$gid" -v admin="$admin" '$4 == gid && $3 != 0 && $3 != admin')
	[ -z "$others" ] || return 0
	printf '%s\n' "$gid"
}

# changeable <tree|folder> <path>: print the first file or folder of the tree, or the folder itself, that a user other than root and the
# admin can change: write for the group unless it is the private group of the admin, write for every user, or another owner.
# A sticky folder above a tree does not let a user rename the files of others, so it does not count.
# The owner of a folder comes from stat. A folder whose owner stat cannot read counts as a folder of another owner.
changeable() {
	set -- "$1" "$2" -perm -020
	[ -z "$admin_group" ] || set -- "$@" ! -group "$admin_group"
	kind=$1
	path=$2
	shift 2
	case $kind in
	tree) find "$path" -xdev ! -type l \( \( "$@" \) -o -perm -002 -o \( ! -user 0 ! -user "$admin" \) \) -print 2>/dev/null | head -n 1 ;;
	folder)
		owner=$(stat -c %u -- "$path" 2>/dev/null) || owner=
		case $owner in
		0 | "$admin") find "$path" -maxdepth 0 \( \( "$@" \) -o -perm -002 \) ! -perm -1000 -print 2>/dev/null | head -n 1 ;;
		*) printf '%s\n' "$path" ;;
		esac
		;;
	esac
}

# changeable_up <folder>: print the first folder that a user other than root and the admin can change, from the real path of the folder up
# to the top. A folder above the top is not judged, except when the real path is outside the top: then the walk goes up to /.
changeable_up() {
	dir=$(realpath -m -- "$1") || {
		printf '%s\n' "$1"
		return 0
	}
	while :; do
		found=$(changeable folder "$dir")
		if [ -n "$found" ]; then
			printf '%s\n' "$found"
			return 0
		fi
		[ "$dir" != / ] && [ "$dir" != "$top" ] || return 0
		dir=$(dirname "$dir")
	done
}

# check_root_paths fails when a user other than root and the admin user (the one that ran sudo) can change a file of the clone, of
# /usr/local/go or of /usr/local/bin, or a folder above one of them up to the top. Root runs the code there, so such a file is a way to root.
# The top is / on a host and HOST_SETUP_SYSROOT in the tests. A path that does not exist yet, and is no link, is skipped: root makes it.
# A path that is a link is judged at its target, with the folders above the target, because root runs the code of the target. A link
# whose target does not exist yet is judged the same way: root would make the target. The folders above the path as written are judged
# too, each at its real path, because the folder that holds a link decides who can replace the link. For a chain of links, the folder
# of each link in the chain is judged.
check_root_paths() {
	admin=${SUDO_UID:-0}
	admin_group=$(admin_private_group)
	top=${sysroot:-/}
	for start in "$root" "$sysroot/usr/local/go" "$sysroot/usr/local/bin"; do
		[ -e "$start" ] || [ -L "$start" ] || continue
		# Each link of a chain, and its folder. The count runs before realpath, because realpath -m fails on a loop in some coreutils.
		found=
		hop=$start
		hops=0
		while [ -z "$found" ] && [ -L "$hop" ]; do
			hops=$((hops + 1))
			[ "$hops" -le 40 ] || fail "the path $start is a chain of more than 40 links, or a loop of links. Point it at a folder with at most a few links"
			found=$(changeable_up "$(dirname "$hop")")
			target=$(readlink -- "$hop") || fail "the link $hop cannot be read"
			case $target in
			/*) hop=$target ;;
			*) hop=$(dirname "$hop")/$target ;;
			esac
		done
		if [ -z "$found" ]; then
			real=$(realpath -m -- "$start") || fail "the path $start cannot be read"
			found=$(changeable tree "$real")
			dir=$start
			while [ -z "$found" ] && [ "$dir" != / ] && [ "$dir" != "$top" ]; do
				dir=$(dirname "$dir")
				found=$(changeable_up "$dir")
				# A folder that is its own real path has no link above it either, so changeable_up judged everything above it.
				[ "$(realpath -m -- "$dir")" != "$dir" ] || break
			done
			[ -n "$found" ] || [ "$real" = "$start" ] || found=$(changeable_up "$(dirname "$real")")
		fi
		[ -z "$found" ] || fail "$found can be written by a user other than root and the admin, so that user could change code that root runs here. Remove the write access of other users, and run the root stage from a clone that only root or the admin can write"
	done
}

# check_job_key fails unless --job-key names a file of one public key line. A private key must never go to the host.
check_job_key() {
	[ -n "$job_key" ] || fail "--job-key is required: name the public key file of the controller. $usage"
	[ -f "$job_key" ] || fail "the job key file $job_key does not exist"
	if grep -q 'PRIVATE KEY' "$job_key"; then
		fail "the job key file $job_key holds a private key. Name the public key of the controller"
	fi
	[ "$(grep -c . "$job_key")" = 1 ] || fail "the job key file $job_key holds no public key, or more than one line. Name a file with exactly one public key"
	grep -Eq '^(ssh-|ecdsa-|sk-)' "$job_key" || fail "the job key file $job_key holds no public key: its line starts with neither ssh-, ecdsa- nor sk-"
}

watchdog_state() {
	active=$(systemctl is-active "$watchdog" 2>/dev/null || true)
	enabled=$(systemctl is-enabled "$watchdog" 2>/dev/null || true)
	restart=$(systemctl show -p Restart --value "$watchdog" 2>/dev/null || true)
	printf '%s %s Restart=%s\n' "$active" "$enabled" "$restart"
}

check_watchdog() {
	state=$(watchdog_state)
	[ "$state" = 'active enabled Restart=always' ] ||
		fail "no self-delete watchdog runs on this host ($watchdog: $state). Make the host with the host maker that installs it, then run this again"
	say "watchdog $watchdog: $state"
}

wait_user_manager() {
	tries=0
	while [ "$tries" -lt 60 ]; do
		case $(systemctl --user is-system-running 2>/dev/null || true) in
		running | degraded) return 0 ;;
		esac
		tries=$((tries + 1))
		sleep 1
	done
	fail 'the user manager of the login user did not start within 60 seconds. Read systemctl --user status for the cause'
}

# check_linger fails when the root stage did not turn linger on for the user. Linger keeps the user manager, which runs the jobs, after logout.
check_linger() {
	[ "$(loginctl show-user "$user" -p Linger)" = Linger=yes ] || fail "linger is off for $user. Run the root stage first: it turns linger on for the job user"
	wait_user_manager
}

# go_bin prints the bin folder of the first GOPATH entry, where go install and make contract-tools-install put the tools.
go_bin() {
	gopath=$("$go_root/bin/go" env GOPATH) || fail 'go env GOPATH failed'
	[ -n "$gopath" ] || fail 'go env GOPATH printed nothing'
	printf '%s/bin\n' "${gopath%%:*}"
}

# set_unit_path writes the PATH of the user manager, so a job finds the tools. A daemon-reload keeps the running units.
# systemd reads the environment.d files in file name order and the last PATH line wins. The name zz- sorts after every file
# that has a number prefix, so this file is the last one on any distribution, unless a file with a letter prefix after zz sets PATH.
set_unit_path() {
	content="PATH=$go_root/bin:$gobin:\${PATH}
BEEHEST_BD_132=$gobin/bd"
	if [ -f "$env_file" ] && [ "$(cat "$env_file")" = "$content" ]; then
		say "$env_file is up to date"
	else
		mkdir -p "$(dirname "$env_file")"
		printf '%s\n' "$content" >"$env_file.tmp"
		mv "$env_file.tmp" "$env_file"
		systemctl --user daemon-reload
		say "wrote $env_file"
	fi
	check_unit_path
}

# check_unit_path fails unless the PATH of the user manager starts with the Go folders, so every job unit finds the tools.
check_unit_path() {
	unit_path=$(systemctl --user show-environment | sed -n 's/^PATH=//p')
	case $unit_path in
	"$go_root/bin:$gobin:"*) ;;
	*) fail "the PATH of the user manager does not start with $go_root/bin:$gobin. An environment.d file that sorts after $(basename "$env_file") sets PATH. Find it with grep -l '^PATH=' in ~/.config/environment.d, /etc/environment.d, /run/environment.d, /usr/local/lib/environment.d and /usr/lib/environment.d, and remove its PATH line or rename it" ;;
	esac
}

# make_work_root makes a missing work root as the login user. It reads the folder again first, and it leaves an existing one as it is.
# It uses no sudo and no chown, so it can make a folder only where the login user can, and it changes no owner.
# It makes the last folder only, with mkdir and no -p. mkdir fails on a path that exists, so a folder or a link that appears there
# between the check and the mkdir makes the step fail instead of taking it over.
make_work_root() {
	check_work_root_folder
	if [ ! -e "$work_root" ]; then
		say "making the work root $work_root"
		mkdir -m 0755 -- "$work_root"
	fi
	[ -d "$work_root/.beehest" ] || mkdir -- "$work_root/.beehest"
}

install_project_tools() {
	PATH=$go_root/bin:$gobin:$PATH
	BEEHEST_BD_132=$gobin/bd
	export PATH BEEHEST_BD_132
	make setup
	make contract-tools-install
}

# make_pin <variable>: print the version that a `<variable> := v<version>` line of the Makefile pins. A value `<module>@v<version>` pins the part after the @.
make_pin() {
	line=$(grep -m 1 -E "^$1 :=" "$root/Makefile") || fail "the Makefile has no $1 line"
	value=${line#*:= }
	value=${value##*@}
	printf '%s\n' "${value#v}"
}

# script_pin <variable>: print the version that a `<variable>=<version>` line of scripts/host-tools.sh pins.
script_pin() {
	line=$(grep -m 1 -E "^$1=" "$root/scripts/host-tools.sh") || fail "scripts/host-tools.sh has no $1 line"
	value=${line#*=}
	printf '%s\n' "${value#v}"
}

go_pin() {
	line=$(grep -m 1 -E '^toolchain go[0-9]' "$root/go.mod") || fail "$root/go.mod has no toolchain line"
	printf '%s\n' "${line#toolchain go}"
}

# module_version <tool>: the command that prints "<module> <version>" from the mod line of `go version -m`, for a tool that go install builds.
# A binary with no mod line prints "no mod line" and fails.
# The Go linker writes that line, so it names the pin of such a tool. The tool's own version text says "dev" or nothing.
module_version() {
	printf '%s\n' "go version -m \"\$(command -v $1)\" | awk -F '\\t' '\$2 == \"mod\" { print \$3, \$4; found = 1; exit } END { if (!found) { print \"no mod line\"; exit 1 } }'"
}

# tools prints one line per tool, split by tabs: the name, how its version is read (text: the first line of the command, mod: the mod line
# of the command), the command that prints its version, and the pin ("-" for none).
tools() {
	go_v=$(go_pin)
	lint_v=$(make_pin GOLANGCI_LINT_VERSION)
	rulez_v=$(make_pin AI_RULEZ_VERSION)
	gotestsum_v=$(make_pin GOTESTSUM_VERSION)
	gitleaks_v=$(make_pin GITLEAKS_VERSION)
	gremlins_v=$(make_pin GREMLINS_VERSION)
	coverage_v=$(make_pin GO_TEST_COVERAGE_VERSION)
	govulncheck_v=$(make_pin GOVULNCHECK_VERSION)
	deadcode_v=$(make_pin DEADCODE_VERSION)
	uv_v=$(script_pin uv_version)
	actionlint_v=$(script_pin actionlint_version)
	printf '%s\t%s\t%s\t%s\n' \
		go text 'go version' "$go_v" \
		golangci-lint mod "$(module_version golangci-lint)" "v$lint_v" \
		ai-rulez text 'ai-rulez --version' "$rulez_v" \
		gotestsum mod "$(module_version gotestsum)" "v$gotestsum_v" \
		gitleaks mod "$(module_version gitleaks)" "v$gitleaks_v" \
		gremlins mod "$(module_version gremlins)" "v$gremlins_v" \
		go-test-coverage mod "$(module_version go-test-coverage)" "v$coverage_v" \
		govulncheck mod "$(module_version govulncheck)" "v$govulncheck_v" \
		deadcode mod "$(module_version deadcode)" "v$deadcode_v" \
		dolt text 'dolt version' - \
		bd text 'bd --version' - \
		jq text 'jq --version' - \
		uv text 'uv --version' "$uv_v" \
		shellcheck text 'shellcheck --version | grep -m 1 "^version:"' - \
		actionlint mod "$(module_version actionlint)" "v$actionlint_v" \
		bats text 'bats --version' -
}

# in_unit <command>: run a command as a transient user unit, the way the ssh adapter runs a job, so it gets the PATH of the user manager.
# The unit reads no input: --pipe gives it the input of the caller, which is the tool list of prove_tools.
in_unit() {
	encoded=$(printf '%s' "$1" | base64 | tr -d '\n')
	systemd-run --user --wait --pipe --quiet --collect -p WorkingDirectory="$root" \
		sh -c "eval \"\$(printf %s $encoded | base64 -d)\"" </dev/null
}

# at_pin <kind> <first line> <pin>: succeed when the version that the first line shows is the pin. A mod line is "<module> <version>", and its
# version must equal the pin exactly, so a pseudo-version or (devel) is not the pin. A text line must hold the pin as a whole number.
at_pin() {
	case $1 in
	mod) [ "${2##* }" = "$3" ] ;;
	*) printf '%s\n' "$2" | grep -Eo '[0-9]+(\.[0-9]+)+' | grep -qxF "$3" ;;
	esac
}

# prove_tool <name> <kind> <command> <pin>: fail unless the tool runs in a user unit, at its pin when it has one. Print its record line.
prove_tool() {
	out=$(in_unit "$3" 2>&1) || fail "$1 does not run in a user unit: $(printf '%s\n' "$out" | head -n 1)"
	first=$(printf '%s\n' "$out" | head -n 1)
	if [ "$4" != - ] && ! at_pin "$2" "$first" "$4"; then
		fail "$1 is not at its pin ${4#v}: a user unit found \"$first\". Run this again after the pin is installed"
	fi
	printf 'tool %s %s\n' "$1" "$first"
}

prove_tools() {
	pins=$(tools)
	tab=$(printf '\t')
	printf '%s\n' "$pins" | while IFS=$tab read -r name kind check pin; do
		prove_tool "$name" "$kind" "$check" "$pin"
	done
}

systemd_version() { systemd-run --version | head -n 1 | cut -d ' ' -f 2; }

# check_up runs the reads of the adapter's Up, so a pass here means Up passes.
check_up() {
	[ "$(loginctl show-user "$user" -p Linger)" = Linger=yes ] || fail "linger is still off for $user"
	case $(systemctl --user is-system-running 2>/dev/null || true) in
	running | degraded) ;;
	*) fail 'the user manager does not run' ;;
	esac
	version=$(systemd_version)
	case $version in
	'' | *[!0-9]*) fail "systemd-run --version gave no version: $version" ;;
	esac
	[ "$version" -ge "$min_systemd" ] || fail "systemd $version is older than $min_systemd, which the ssh adapter needs"
	[ -d "$work_root" ] || fail "the work root $work_root does not exist"
}

# landlock prints "landlock pass", or "landlock warning <lsm list> <kernel>" when the kernel cannot sandbox an agent run.
landlock() {
	lsm=$(cat "$lsm_file" 2>/dev/null || echo none)
	kernel=$(uname -r)
	release=${kernel%%-*}
	major=${release%%.*}
	minor=${release#*.}
	minor=${minor%%.*}
	case ",$lsm," in
	*,landlock,*)
		if [ "$major" -gt "$landlock_major" ] || { [ "$major" -eq "$landlock_major" ] && [ "$minor" -ge "$landlock_minor" ]; }; then
			echo 'landlock pass'
			return 0
		fi
		;;
	esac
	printf 'landlock warning %s %s\n' "$lsm" "$kernel"
}

# guard_differs <source> <destination>: succeed when the destination is missing or differs from the source.
guard_differs() {
	[ ! -f "$2" ] || ! cmp -s "$1" "$2"
}

# install_guard installs the rules file and the unit that load it before the network and before any user session, as root, and starts it.
# A file that is already the same is not written again, and a unit that runs with the same files is not restarted.
install_guard() {
	changed=
	if guard_differs "$root/scripts/metadata-guard.nft" "$guard_rules"; then
		sudo install -D -m 0644 "$root/scripts/metadata-guard.nft" "$guard_rules"
		changed=1
	fi
	if guard_differs "$root/scripts/metadata-guard.service" "$guard_unit_file"; then
		sudo install -D -m 0644 "$root/scripts/metadata-guard.service" "$guard_unit_file"
		sudo systemctl daemon-reload
		changed=1
	fi
	sudo systemctl enable "$guard_unit"
	if [ -n "$changed" ] || ! systemctl is-active --quiet "$guard_unit"; then
		say "starting $guard_unit"
		sudo systemctl restart "$guard_unit"
	else
		say "$guard_unit is up to date"
	fi
}

token_path=/computeMetadata/v1/instance/service-accounts/default/token

# check_guard_root fails unless the unit runs and is enabled, the rules are loaded, and root gets the token. Root must get HTTP 200:
# the idle watchdog runs as root and needs the server. Any status other than 200, even 403, fails. It prints the result.
check_guard_root() {
	[ "$(systemctl is-active "$guard_unit" 2>/dev/null || true)" = active ] || fail "$guard_unit is not active"
	[ "$(systemctl is-enabled "$guard_unit" 2>/dev/null || true)" = enabled ] || fail "$guard_unit is not enabled, so a reboot would drop the rules"
	sudo nft list table inet beehest_metadata_guard >/dev/null || fail 'the metadata guard rules are not loaded'
	url=http://169.254.169.254$token_path
	code=$(sudo curl -g -s -o /dev/null --max-time 5 -H 'Metadata-Flavor: Google' -w '%{http_code}' "$url") || true
	[ "$code" = 200 ] || fail "root got HTTP $code from $url, not 200. The idle watchdog runs as root and needs the metadata server. Read $guard_rules"
	printf 'metadata root=%s\n' "$code"
}

# probe_in_unit <url>: print "<HTTP status> <curl exit code>" for the token path, run in a user unit like a job. Any status, even 403, means the server answered.
probe_in_unit() {
	in_unit "code=\$(curl -g -s -k -o /dev/null --max-time 5 -H 'Metadata-Flavor: Google' -w '%{http_code}' '$1') && rc=0 || rc=\$?; echo \"\$code \$rc\""
}

# check_metadata_refused fails unless the job user gets no answer from the metadata server on port 80 or 443, over IPv4 or IPv6, in a user unit.
# A refused connection (curl 7) and a timeout (28) are no answer. Any other curl failure proves nothing, so it fails the script.
# It prints the record line.
check_metadata_refused() {
	for url in "http://169.254.169.254$token_path" "http://[fd20:ce::254]$token_path" "https://169.254.169.254$token_path" "https://[fd20:ce::254]$token_path"; do
		out=$(probe_in_unit "$url") || fail "the check of $url did not run in a user unit"
		code=${out% *}
		rc=${out##* }
		case $rc in
		7 | 28) ;;
		0) fail "the job user got HTTP $code from $url. A step of a gate can take the token of the machine. Read $guard_rules" ;;
		*) fail "the check of $url ended with curl exit code $rc, which proves nothing. Run curl -g -k --max-time 5 $url" ;;
		esac
	done
	printf 'metadata user=refused sudo=none\n'
}

# make_job_user makes the job user with useradd, which gives it no sudoers entry and no group but its own. The guest agent adds the users
# that it makes to google-sudoers, and it does not touch a user that useradd made. The key of the job user goes in no instance metadata.
make_job_user() {
	if id -u "$job_user" >/dev/null 2>&1; then
		say "the job user $job_user exists"
	else
		say "making the job user $job_user"
		sudo useradd --create-home --home-dir "$job_home" --shell /bin/sh "$job_user"
	fi
	reject_root_groups "the job user $job_user" "$job_user" "$(id -nG "$job_user")"
}

# install_job_key makes the key of the controller the only key of the job user. A key that is already there is not written again.
# Root copies the key to a root folder that every user can read, and the job user writes it into its home: the job user owns that home,
# so a link it puts there leads only to files that it can write itself.
install_job_key() {
	if [ -f "$key_file" ] && cmp -s "$job_key" "$key_file"; then
		say "$key_file is up to date"
		return 0
	fi
	say "writing $key_file"
	sudo install -D -m 0644 "$job_key" "$job_key_copy"
	sudo -u "$job_user" install -d -m 0700 "$(dirname "$key_file")"
	sudo -u "$job_user" install -m 0600 "$job_key_copy" "$key_file"
}

# enable_job_linger turns linger on for the job user, so its user manager runs the jobs with no login.
enable_job_linger() {
	if [ "$(loginctl show-user "$job_user" -p Linger 2>/dev/null || true)" != Linger=yes ]; then
		say "turning on linger for $job_user"
		sudo loginctl enable-linger "$job_user"
	fi
}

write_record() {
	record=$work_root/.beehest/host-setup.record
	{
		printf 'at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		printf 'commit=%s\n' "$(git -C "$root" rev-parse HEAD)"
		printf 'kernel=%s\n' "$(uname -r)"
		printf 'systemd=%s\n' "$(systemd_version)"
		landlock
		printf 'watchdog %s %s\n' "$watchdog" "$(watchdog_state)"
		printf '%s\n' "$guard_line"
		printf '%s\n' "$1"
	} >"$record.tmp"
	mv "$record.tmp" "$record"
	cat "$record"
}

root_stage() {
	refuse_root
	check_watchdog
	sh "$root/scripts/host-tools.sh" system
	install_guard
	make_job_user
	install_job_key
	enable_job_linger
	check_guard_root
	check_watchdog
	echo HOST-SETUP-ROOT-DONE
}

job_stage() {
	refuse_job
	check_watchdog
	user=$(id -un)
	sh "$root/scripts/host-tools.sh" user
	gobin=$(go_bin)
	check_linger
	set_unit_path
	make_work_root
	install_project_tools
	proof=$(prove_tools)
	check_up
	check_no_way_to_root
	guard_line=$(check_metadata_refused)
	write_record "$proof"
	check_watchdog
	echo HOST-SETUP-DONE
}

read_args "$@"
case $stage in
root) root_stage ;;
job) job_stage ;;
esac
