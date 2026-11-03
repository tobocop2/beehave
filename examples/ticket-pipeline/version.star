# Raise the version when a prompt, the label set or a script changes the result of a day. The load records it, and the plan skips days at this version.
# Set PIPE_VERSION in the environment of the run to change it with no file edit.
VERSION = env("PIPE_VERSION", default = "2")

# The days in one chunk. One chunk is one child run of the backfill, so the history of a run holds a bounded number of steps.
CHUNK = 100

# The largest range that the plan accepts.
MAX_DAYS = 5000
