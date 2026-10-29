# Agent logins that obee can use. One login for one run: obee never
# switches login inside one lane.
# No token or real account name belongs in this file.
# obee sets CLAUDE_CONFIG_DIR to the config_dir of the chosen runner in the run environment.
# The Claude CLI then uses the login stored for that folder. Log in once with:
#   CLAUDE_CONFIG_DIR=<config_dir> claude
# This file calls three constructors: pool(), agents() and runner(). Each takes keyword arguments only.
# Arguments of runner(): name (once each), agent (claude or opencode), config_dir, keychain.
# keychain is optional. It names the macOS Keychain entry that holds a login token, for a machine
# with no logged-in profile. Make the token with `CLAUDE_CONFIG_DIR=<config_dir> claude setup-token`
# and store it with:
#   security add-generic-password -s <keychain> -a $USER -w

# pool() sets the usage caps, in percent. `obee run` picks the Claude runner that is under
# both caps, with the lowest seven-day use, then the lowest five-hour use. A runner whose usage
# cannot be read, or that is not logged in, is never picked; `obee doctor` names it. When no runner is under both caps, the run stops
# with exit 3 and names the earliest reset time. Both arguments are optional. Call pool() once.
pool(
    five_hour_max = 90,
    seven_day_max = 95,
)

# max_concurrent in agents() is the global limit on agent processes. A laptop wants 2, a
# build host more. Each login runs one agent at a time. The argument is optional; the default is 2.
# Call agents() once.
agents(
    max_concurrent = 2,
)

runner(
    name = "claude-a",                  # your own label
    agent = "claude",
    config_dir = "~/.claude-profiles/a",  # one Claude config folder for each login
    # keychain = "obee-claude-a",       # optional
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
