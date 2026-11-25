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
    # pr_label = "made-by-beehave",  # optional; the label of every item pull request that the engine opens. The default is "made-by-beehave". An empty value is refused. The forge creates the label when the repository has none.
    # max_spawn_depth = 3,  # optional; the deepest level below the run a user started at which spawn may start a run. The default is 3. Set 1 to let a run spawn but stop its children from spawning. A value below 1 is refused.
    # pin_file =".tool-versions",  # optional; a relative path with no `..`. beehave reads the file at origin/<base_branch>, not in the work tree of the lane, one tool and its exact version on each line ("1.27.3", not "1.27"). Before a local gate runs, beehave asks each tool for its version by name, in the directory and the environment of the gate. A gate that sets its own PATH or runs the tool through a wrapper is not checked. A tool at another version, or one that gives no version, voids the gate: every step is unknown and the gate command does not run. A gate on a build host runs the same check on the host
    # gate_program = ["Makefile", "scripts/"],  # optional; the repository paths that hold the program of the gate, with no `..` and no glob. A directory ends in `/`. A path that the base branch lacks is legal and means the path must stay absent. A project with no `source` and no `forge` refuses it. The config reads the key; the gate does not use it yet
    # sandbox = "on",  # optional; "on" or "off". "on" runs the shell of each agent step that sets no `sandbox` in the Claude Code sandbox: no network, and no read outside the work folder. A project that leaves it out gets "on". "off" runs those steps with no sandbox, for a project whose steps cannot run in it, such as one with an OpenCode step or one on Linux with no bwrap and socat. A step sets `sandbox = sandbox(network = [...])` to name the hosts that its shell reaches
    # sandbox_read = ["~/sdk"],         # optional; extra paths that a sandboxed shell can read. Absolute, or starting with ~/
    # sandbox_deny = ["~/.config/vault"],  # optional; extra credential paths, added to the built-in list
    # gate_network = ["proxy.golang.org"],  # optional; domains that the gate command can reach when the project has `sandbox = "on"` and the gate runs on this machine. The default is no network.
    # gate_allow_write = ["~/go/pkg"],      # optional; extra paths that the gate command can write. Relative paths start at the work folder.
    # sandbox_enforcer = "auto",            # optional; "auto" (the default) picks the best enforcer that can run here. "landlock" names one (Linux 6.7 or later).
    # vars holds the free settings of your workflow: any string keys, with None, bool, string, number, list and dict values. `facts().vars` returns them as written. The engine reads no key.
    # The remediation example reads these keys from it, and puts them into the prompt of each agent. A key that vars leaves out is empty.
    # vars = {
    #     "people": {"owner": "your-login", "third_party_idle_hours": "24"},  # a role to a login. The entry third_party_idle_hours is text, not a number: the idle hours after which a pull request of a third party counts as stale
    #     "epics": {"bugs": "proj-1"},                  # a queue name to its epic
    #     "gate_files": [".github/workflows/ci.yml"],   # the CI workflow files that the round derives its gate list from; the project then needs gate_steps. A job that can write or reads a secret, and a step that uses an action, are left out of the list and named in the output
    #     "gate_map": {"make test": ["test"], "go install x@v1": []},  # each derived CI command, exactly as the list prints it (the run text, then its shell, folder, env names, container, continue-on-error and if when set), to the gate_steps that run it, or [] when it is not a check; the round stops when a command has no key, a key is stale or a name is no gate step
    #     "conventions": ["AGENTS.md"],                 # the files to review against
    #     "brief_rules": ["Name the tempting wrong fix"],     # rules for the brief of an agent
    #     "stacked_rules": ["Merge the base pull request first"],  # rules for a pull request that stacks on another
    #     "limits": ["Never run rm on a glob"],         # hard limits, commands never run and real user data
    #     "traps": ["The build cache hides a stale tree"],    # measured traps and ownership rules
    #     "token_rules": ["Read a log by its summary lines"],  # token discipline, the dashboard, cross-repository steps and release steps
    #     "changelog": "CHANGELOG.md",                  # the changelog file; the example workflow refuses a merge that leaves it untouched
    #     "mutation_command": ["sh", "scripts/mutation-pr.sh"],  # required by the example: the command of your mutation tool, a list of words. It runs on the build host in the slot class "mutation" (see the host block below), with the base ref as its last word
    #     "review_skill": "your-review-skill",  # required by the example: the name of the skill that the sweep step loads after the implement step. The engine starts the sweep with this skill loaded
    #     "board": {
    #         "number": 3,                  # the number of the GitHub Project (v2) board; required
    #         "started": "In Progress",     # the Status column that the remediation example sets when the draft pull request opens; required
    #         "merged": "Done",             # the Status column that it sets after the merge; required
    #         "owner": "your-org",          # the login that owns the board; the default is the owner of `repo`
    #     },                                # optional. With no board key the example makes no board call. The token needs the `project` scope
    # },
    # The remediation example reads the settings of a round from the key `round` of `vars`. The engine reads none of them, and the example refuses a missing or wrong one. A project has one `vars`, so put the key `round` in the same dict as your other keys:
    # vars = {"round": {
    #     "base": "integration/r2",         # the branch of the round; the round id is round/<base>; required
    #     "owner": "your-login",            # the login of the owner; required
    #     "lane_cap": 4,                    # the lanes that run at once, 1 or more; required
    #     "fix_rounds": 5,                  # the fix rounds for one item, 1 or more; required
    #     "label": "beehave-round",         # the tracker label that selects the items; set this or "items", not both; required
    #     # "items": ["12", "15"],          # the explicit list of item ids, in place of "label"
    #     "next_base": "integration/r3",    # the branch of the next round; empty means the round does not cut
    #     "merge": False,                   # True lets the round land approved items; the default is False
    #     "hand_off": False,                # True: the owner merges; the round marks the approved pull requests ready and waits, with merge True
    #     "integration_pr": 0,              # a number pins the integration pull request; 0 lets the round find it
    #     "wait_hours": 24,                 # the hours that the round waits for the owner, 1 or more; the default is 24
    #     "lane_kind": "impl",              # impl, papers or design; papers and design need "allow_paths"
    #     "steps": {"review": {"model": "your-model", "runner": "opencode-1"}},  # the model and the runner of a step; a step left out keeps its default model and the pool pick
    #     "design_after": 2,                # the fixes on one mechanism before the design step, 1 to 100
    #     "void_reruns": 2, "void_asks": 3, # the reruns and the owner questions for a void gate, 0 to 100
    #     "escalate_after": 3,              # the last fix round on the step "fix"; later rounds start fresh on "fix_late", 0 to 100
    # }},                                 # optional. Also: owner_email, source_checkout, acceptance, base_result, base_reds, base_reds_file, allow_paths
    build_host = "local",   # "local", or the name of a build host (a SkyPilot cluster, or the ssh alias of a machine or of the head node of a Slurm cluster; a letter, then letters, digits, dots and hyphens, with no trailing dot, no `..` and no `.-`), or a list of names such as ["gpu", "gpu-b"]
    # The first name of a list is the default. A run chooses another with the host argument of host_up. All the clusters share the host block below.
    # The host argument is required when build_host is a cluster name, and refused when build_host is "local".
    # host = host(
    #     provider = "skypilot",   # the host adapter
    #     idle_minutes = 60,       # the host is deleted after this many idle minutes (default 60)
    #     stale_minutes = 20,      # a running job whose log does not grow for this many minutes is stopped (default 20)
    #     max_run = "24h",         # the host is deleted after this run time (default "24h"); SkyPilot documents this limit for GPU instances only
    #     disk_floor_gb = 50,      # a host with less free space gets no new job; required
    #     disk_wait_minutes = 10,  # a job under the floor waits this many minutes for space, then is refused (default 0: refused at once)
    #     slots = {"mutation": 1}, # at most this many host jobs of a class at once, for a command call with slots = "<class>" (default: no class)
    #     gate_slots = 1,          # at most this many gates build on the host at once, from 1 up (default 1); projects that share a host give the same value
    #     bank_budget = "20s",     # the bank scan gives each repository this long for all its git calls; a repository that is slower counts as unbanked (default "20s")
    #     lost_job_wait = "60m",   # how long a job that lost its link waits for the proof that it no longer runs; from "1m" to "24h" (default "60m")
    #     lost_after = "5m",       # how long a job gets no answer to a read before the wait ends as lost; above zero (default "5m")
    #     stop_limit = "5m",       # how long a job may take to end after a cancel; above zero (default "5m")
    #     private_remote = "git@example.com:your-org/your-repo-private.git",  # where beehave pushes unbanked work before the host goes down
    #     setup_command = "make setup",  # runs once in a clone of the project when the cluster is created; skypilot only (default: no set-up)
    #     skypilot = skypilot(
    #         cloud = "gcp",       # empty leaves the choice to SkyPilot
    #         region = "us-central1",
    #         instance_type = "n2-standard-8",   # one size for every queue; or give instance_sizes, never both
    #         disk_size_gb = 100,  # boot disk size, 10 to 65536 (default: the size SkyPilot picks)
    #         spot = True,         # a spot instance; True or False (default False). A spot cluster has no max_run setting; the queue still ends each job at max_run
    #         # instance_sizes = "3:n2-standard-4,n2-standard-16",  # up to 3 waiting gates: the first type; more: the last type. The first gate starts the host
    #     ),
    # ),
    # A machine you own: provider = "ssh" takes the ssh block and refuses idle_minutes and the block of any other provider. beehave never deletes the machine: host_teardown banks the work and leaves it up.
    # A Slurm cluster you own: provider = "slurm" takes the slurm block, with the same keys as the ssh block. It names the head node (build_host is its ssh alias). Jobs run in the login folder, so slurm refuses work_root.
    # max_run goes to sbatch as --time. The cluster is yours, so no slot job holds it and host_teardown banks the work and leaves it up.
    # host = host(
    #     provider = "ssh",
    #     disk_floor_gb = 50,
    #     credentials = "none",         # optional, for a host that runs code you do not trust: it gets no SSH agent, key, X display, Kerberos credential or secret variable of yours. check config refuses it when ssh -G shows one, or a jump host

    #     ssh = ssh(
    #         address = "10.0.0.5",         # the name or IP to connect to; the default is the ssh alias in build_host
    #         user = "ci",                  # the login user; the default is your ssh config
    #         port = 22,                    # 1 to 65535; the default is your ssh config
    #         identity = "~/.ssh/ci",       # a private key file, absolute or under ~/; the default is your ssh config and agent
    #         work_root = "/home/ci/work",  # where the repositories live; the default is the login folder
    #         known_hosts = "strict",       # "strict" (default) or "accept-new"; a host key that changes is always refused
    #     ),
    # ),
    # The same for a Slurm cluster. The slurm block takes the keys of the ssh block, except work_root.
    # host = host(
    #     provider = "slurm",
    #     disk_floor_gb = 50,
    #     slurm = slurm(
    #         address = "head.example.org", # the name or IP of the head node; the default is the ssh alias in build_host
    #         user = "ci",
    #         known_hosts = "strict",
    #     ),
    # ),
)
