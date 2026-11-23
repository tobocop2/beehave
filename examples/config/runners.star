# Agent logins that beehave can use. One login for one run: beehave never
# switches login inside one lane.
# No token or real account name belongs in this file.
# beehave sets CLAUDE_CONFIG_DIR to the config_dir of the chosen runner in the run environment.
# The Claude CLI then uses the login stored for that folder. Log in once with:
#   CLAUDE_CONFIG_DIR=<config_dir> claude
# This file calls three constructors: pool(), agents() and runner(). Each takes keyword arguments only.
# Arguments of runner(): name (once each), agent (claude or opencode), config_dir, keychain.
# keychain is optional. It names the macOS Keychain entry that holds a login token, for a machine
# with no logged-in profile. Make the token with `CLAUDE_CONFIG_DIR=<config_dir> claude setup-token`
# and store it with:
#   security add-generic-password -s <keychain> -a $USER -w

# pool() sets the usage caps, in percent. `beehave run` picks the Claude runner that is under
# both caps, with the lowest seven-day use, then the lowest five-hour use. A runner whose usage
# cannot be read, or that is not logged in, is never picked; `beehave doctor` names it. When no runner is under both caps, the run stops
# with exit 3 and names the earliest reset time. Both arguments are optional. Call pool() once.
pool(
    five_hour_max = 90,
    seven_day_max = 95,
)

# max_concurrent in agents() is the global limit on agent processes. A laptop wants 2, a
# build host more. Each login runs one agent at a time on each queue, so a review step and an implement step of one login can run at once. The argument is optional; the default is 2.
# max_review_concurrent is the same limit for the agent steps with edits = False. The two
# limits are separate: an idle review slot does not run implement work. The default is 2.
# max_turns and max_budget_usd cap one agent run, in agentic turns and in dollars. 0 means no cap,
# which is the default. A run that reaches a cap ends the step with the output stop. Both are optional.
# max_run_budget_usd caps the dollars that all agent steps of one item run spend, failed runs included. An agent step does not start when the sum is at or past
# the cap, so the last step can pass it by its own cost (set max_budget_usd to limit that). A child workflow of the run starts its own sum.
# max_tree_budget_usd caps the same sum for the root run and every run it spawns. A spawned run uses the cap of its root. A step that a cap stops waits:
# raise the cap (or set it to 0) and restart beehave to resume it.
# max_week_budget_usd caps the dollars that the agent steps of all items spend in any seven days. A step that ended exactly seven days ago counts. Keep retention_days at 7 or more, or at 0: a deleted run is not in the sum.
# A step can set a lower cap for itself with max_budget_usd in its agent() call.
# review_poll_minutes is the time between two polls of the open pull requests that wait for your
# review or your reply. 0 means no poll, which is the default. The most is 10080 (one week).
# `beehave status` shows the newest poll.
# review_lookback_days is the number of days that the poll looks back for replies to answer. The default is 3. The least is 1. The most is 365.
# per_login is the most agent steps that one login runs at once, in each of the two agent queues. The default is 1. The least is 1. The most is the lower of max_concurrent and max_review_concurrent.
# concurrency is "static" or "adaptive". The default is "static": the agents queue holds max_concurrent.
# With "adaptive", max_concurrent is the ceiling. The limit starts at per_login, adds one agent each minute while every login has room and a phase waits,
# and halves (never below per_login) when a phase ends at a provider limit. beehave status shows the limit.
# retention_days is the age in days at which `beehave run` deletes a finished run with its recorded steps. The default is 7.
# 0 keeps every finished run. The most is 36500. A failed or canceled run is never deleted.
# trace_parent is true or false. The default is false. When true, each agent process gets one TRACEPARENT that names the run and the step, so the spans of one run share a trace id.
# beehave exports no span and passes no exporter setting: the agent CLI exports its own spans.
# Call agents() once.
agents(
    max_concurrent = 2,
    # max_review_concurrent = 2,
    # per_login = 1,
    # concurrency = "static",
    # max_turns = 60,
    # max_budget_usd = 5,
    # max_run_budget_usd = 20,
    # max_tree_budget_usd = 100,
    # max_week_budget_usd = 300,
    # review_poll_minutes = 60,
    # review_lookback_days = 3,
    # retention_days = 7,
    # trace_parent = False,
)

runner(
    name = "claude-a",                  # your own label
    agent = "claude",
    config_dir = "~/.claude-profiles/a",  # one Claude config folder for each login
    # keychain = "beehave-claude-a",       # optional
)

runner(
    name = "claude-b",
    agent = "claude",
    config_dir = "~/.claude-profiles/b",
)

# Later: a local model through Pi or OpenCode.
# runner(
#     name = "local",
#     agent = "opencode",
# )
