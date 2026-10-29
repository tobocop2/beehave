# One project profile. Make one file for each project, and call project() once in it.
# The arguments of project() are keyword arguments. An unknown argument is refused. build_host is optional; the default is local.
# tracker, claims and forge name the adapters of the project. Each one is optional.
# Name this file in `obee run --project <name>`. The gate step runs `gate` for that project.

project(
    repo = "your-org/your-repo",   # owner/name; required when tracker or forge is "github"
    # source = "~/work/your-repo",  # a git URL or an absolute path (a leading ~ expands); required when the project has no forge
    tracker = "github",        # "local" (default), "beads" or "github" (GitHub Issues); this chooses where work items live
    claims = "beads",          # "local" (default) or "beads"; this chooses where the claim leases live
    forge = "github",          # "github", or leave the argument out for a project with no forge
    beads_prefix = "proj",     # the id prefix of the beads database; required when tracker or claims is "beads"
    base_branch = "main",
    gate = "make check",       # the same command that your CI runs
    build_host = "local",      # "local", or the name of a SkyPilot cluster (not a Slurm partition)
    # The host argument is required when build_host is a cluster name, and refused when build_host is "local".
    # host = host(
    #     provider = "skypilot",   # the host adapter
    #     idle_minutes = 60,       # the host is deleted after this many idle minutes (default 60)
    #     max_run = "24h",         # the host is deleted after this run time (default "24h"); SkyPilot documents this limit for GPU instances only
    #     disk_floor_gb = 50,      # a host with less free space gets no new job; required
    #     private_remote = "git@example.com:your-org/your-repo-private.git",  # where obee pushes unbanked work before the host goes down
    #     skypilot = skypilot(
    #         cloud = "gcp",       # empty leaves the choice to SkyPilot
    #         region = "us-central1",
    #         instance_type = "n2-standard-8",
    #     ),
    # ),
)
