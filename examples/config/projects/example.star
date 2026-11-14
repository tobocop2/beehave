# One project profile. Make one file for each project, and call project() once in it.
# The arguments of project() are keyword arguments. An unknown argument is refused. build_host is optional; the default is local.
# tracker, claims and forge name the adapters of the project. Each one is optional.
# Name this file in `beehave run --project <name>`. The gate step runs `gate` for that project.

project(
    repo = "your-org/your-repo",   # owner/name; required when tracker or forge is "github"
    # source = "~/work/your-repo",  # a git URL or an absolute path (a leading ~ expands); required when the project has no forge
    tracker = "github",        # "local" (default), "beads" or "github" (GitHub Issues); this chooses where work items live
    claims = "beads",          # "local" (default) or "beads"; this chooses where the claim leases live
    forge = "github",          # "github", or leave the argument out for a project with no forge
    beads = beads(prefix = "proj"),  # the beads block: the id prefix of the beads database; required when tracker or claims is "beads", and refused otherwise
    base_branch = "main",
    gate = "make check",       # the same command that your CI runs
    # gate_steps = {"fmt": "make fmt", "lint": "make lint", "test": "make test"},  # instead of `gate`: one named step for each command, in order. The gate gives a result for each name, and a merge needs a pass for every one. The loader refuses `gate` and `gate_steps` together, and the name `tests`
    test_report = "reports/*.xml",  # required; a glob for JUnit XML files, relative to the work tree. A local gate that exits 0 fails when no new file matches, a file does not parse, or the files hold 0 tests. A file that the gate leaves unchanged (same modification time and size) does not count. A gate on a build host fails when the glob matches no testcase element. The loader refuses a project without it
    # visibility = "private",  # optional; "public" or "private". A project that leaves it out counts as public. For a public repository, beehave refuses to copy files from this machine to a build host: the host clones the repository from the forge, and data moves only through a private bucket
    # pin_file =".tool-versions",  # optional; a relative path with no `..`. beehave reads the file at origin/<base_branch>, not in the work tree of the lane, one tool and its exact version on each line ("1.27.3", not "1.27"). Before a local gate runs, beehave asks each tool for its version by name, in the directory and the environment of the gate. A gate that sets its own PATH or runs the tool through a wrapper is not checked. A tool at another version, or one that gives no version, voids the gate: every step is unknown and the gate command does not run. A gate on a build host runs the same check on the host
    # sandbox = "on",  # optional; "on" or "off". "on" runs the shell of each agent step that sets no `sandbox` in the Claude Code sandbox: no network, and no read outside the work folder. A project that leaves it out runs with no sandbox. A step sets `sandbox = sandbox(network = [...])` or `sandbox = None`
    # sandbox_read = ["~/sdk"],         # optional; extra paths that a sandboxed shell can read. Absolute, or starting with ~/
    # sandbox_deny = ["~/.config/vault"],  # optional; extra credential paths, added to the built-in list
    # gate_network = ["proxy.golang.org"],  # optional; domains that the gate command can reach when the project has `sandbox = "on"` and the gate runs on this machine. The default is no network.
    # gate_allow_write = ["~/go/pkg"],      # optional; extra paths that the gate command can write. Relative paths start at the work folder.
    # sandbox_enforcer = "auto",            # optional; "auto" (the default) picks the best enforcer that can run here. "landlock" names one (Linux 6.7 or later).
    # The facts below are optional. The `facts()` value of a workflow file holds them with the keys above (repo, source, forge, tracker, base_branch, gate, build_host), and the remediation example puts them into the prompt of each agent. An argument that the file leaves out is empty.
    # people = {"owner": "your-login", "third_party_idle_hours": "24"},  # a role to a login. The entry third_party_idle_hours is text, not a number: the idle hours after which a pull request of a third party counts as stale
    # epics = {"bugs": "proj-1"},                  # a queue name to its epic
    # gate_files = [".github/workflows/ci.yml"],   # the workflow files that the list of gate commands comes from
    # conventions = ["AGENTS.md"],                 # the files to review against
    # brief_rules = ["Name the tempting wrong fix"],     # rules for the brief of an agent
    # stacked_rules = ["Merge the base pull request first"],  # rules for a pull request that stacks on another
    # limits = ["Never run rm on a glob"],         # hard limits, commands never run and real user data
    # traps = ["The build cache hides a stale tree"],    # measured traps and ownership rules
    # token_rules = ["Read a log by its summary lines"],  # token discipline, the dashboard, cross-repository steps and release steps
    # changelog = "CHANGELOG.md",                  # the changelog file; the example workflow refuses a merge that leaves it untouched
    # board = board(
    #     number = 3,                  # the number of the GitHub Project (v2) board; required
    #     started = "In Progress",     # the Status column that the remediation example sets when the draft pull request opens; required
    #     merged = "Done",             # the Status column that it sets after the merge; required
    #     owner = "your-org",          # the login that owns the board; the default is the owner of `repo`
    # ),                               # optional. With no board block the example makes no board call. The token needs the `project` scope
    # schedules = [
    #     schedule(
    #         name = "weekly-bump",          # lower case letters, digits and hyphens; unique in the project; required
    #         cron = "0 0 9 * * 1",          # six fields with the seconds first, or a descriptor such as "@weekly"; required
    #         workflow = "bump.star",        # a workflow file; a relative path starts in the folder of this file; required
    #         item = "bump",                 # the prefix of the item id; required
    #         timezone = "Europe/Berlin",    # an IANA zone; the default is "UTC"
    #         catch_up = False,              # True asks for the ticks that no engine ran; the default is False
    #     ),
    # ],                               # optional. This key only declares the schedules and `beehave check config` validates them
    build_host = "local",   # "local", or the name of a build host (a SkyPilot cluster, or the ssh alias of a machine or of the head node of a Slurm cluster; a letter, then letters, digits, dots and hyphens), or a list of names such as ["gpu", "gpu-b"]
    # The first name of a list is the default. A run chooses another with the host argument of host_up. All the clusters share the host block below.
    # The host argument is required when build_host is a cluster name, and refused when build_host is "local".
    # host = host(
    #     provider = "skypilot",   # the host adapter
    #     idle_minutes = 60,       # the host is deleted after this many idle minutes (default 60)
    #     stale_minutes = 20,      # a running job whose log does not grow for this many minutes is stopped (default 20)
    #     max_run = "24h",         # the host is deleted after this run time (default "24h"); SkyPilot documents this limit for GPU instances only
    #     disk_floor_gb = 50,      # a host with less free space gets no new job; required
    #     disk_wait_minutes = 10,  # a job under the floor waits this many minutes for space, then is refused (default 0: refused at once)
    #     bank_budget = "20s",     # the bank scan gives each repository this long for all its git calls; a repository that is slower counts as unbanked (default "20s")
    #     private_remote = "git@example.com:your-org/your-repo-private.git",  # where beehave pushes unbanked work before the host goes down
    #     skypilot = skypilot(
    #         cloud = "gcp",       # empty leaves the choice to SkyPilot
    #         region = "us-central1",
    #         instance_type = "n2-standard-8",
    #     ),
    # ),
    # A machine you own: provider = "ssh" takes the ssh block and refuses idle_minutes and the block of any other provider. beehave never deletes the machine: host_teardown banks the work and leaves it up.
    # A Slurm cluster you own: provider = "slurm" takes the slurm block, with the same keys as the ssh block. It names the head node (build_host is its ssh alias). Jobs run in the login folder, so slurm refuses work_root.
    # max_run goes to sbatch as --time. The cluster is yours, so no slot job holds it and host_teardown banks the work and leaves it up.
    # host = host(
    #     provider = "ssh",
    #     disk_floor_gb = 50,
    #     ssh = ssh(
    #         address = "10.0.0.5",         # the name or IP to connect to; the default is the ssh alias in build_host
    #         user = "ci",                  # the login user; the default is your ssh config
    #         port = 22,                    # 1 to 65535; the default is your ssh config
    #         identity = "~/.ssh/ci",       # a private key file, absolute or under ~/; the default is your ssh config and agent
    #         work_root = "/home/ci/work",  # where the repositories live; the default is the login folder
    #         known_hosts = "strict",       # "strict" (default) or "accept-new"; a host key that changes is always refused
    #     ),
    # ),
)
