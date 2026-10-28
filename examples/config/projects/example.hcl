# One project profile. Make one file for each project.
# The keys of a project. An unknown key is refused. build_host is optional; the default is local.
# tracker and forge name the adapters of the project. No adapter is a default.
# Name this file in `obee run --project <name>`. The gate step runs `gate` for that project.

repo         = "your-org/your-repo"
tracker      = "github"        # "beads" or "github" (GitHub Issues); this chooses where work items live
forge        = "github"        # "github"
beads_prefix = "proj"          # the id prefix of the beads database. The claim leases live in beads for every tracker.
base_branch  = "main"
gate         = "make check"    # the same command that your CI runs
build_host   = "local"         # "local", or the name of a SkyPilot cluster (not a Slurm partition)

# The host block is required when build_host is a cluster name, and refused when build_host is "local".
# host {
#   provider       = "skypilot"   # the host adapter
#   idle_minutes   = 60           # the host is deleted after this many idle minutes (default 60)
#   max_run        = "24h"        # the host is deleted after this run time (default "24h"); SkyPilot documents this limit for GPU instances only
#   disk_floor_gb  = 50           # a host with less free space gets no new job; required
#   private_remote = "git@example.com:your-org/your-repo-private.git"  # where obee pushes unbanked work before the host goes down
#   skypilot {
#     cloud         = "gcp"       # empty leaves the choice to SkyPilot
#     region        = "us-central1"
#     instance_type = "n2-standard-8"
#   }
# }
