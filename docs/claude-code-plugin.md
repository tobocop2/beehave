# The Claude Code plugin

The plugin in `plugins/claude-code` lets a user drive obee from a Claude Code chat. It holds no method. Each slash command shows an obee command, runs it, and reports the output.

## Before you start

- Install `obee` and put it on the `PATH`. Run `make build` and use `bin/obee`, or install a release.
- Set up the config folder as in `README.md`. The plugin passes `--config` through when the user gives it.
- `obee run` needs a workflow file: `workflow.star` in the current folder of the chat, or the file that `--workflow` names. It needs a project when the config holds more than one. If obee names one as missing, the command asks the user for it.

## Install from a local checkout

To load the plugin for one session, name its folder:

```bash
claude --plugin-dir /path/to/obee/plugins/claude-code
```

To install the plugin, add the folder as a marketplace, then install from it:

```bash
claude plugin marketplace add /path/to/obee/plugins/claude-code
claude plugin install obee@obee-local
```

A local marketplace reads the files in place. After an edit, run `/reload-plugins` in the session.

To check the plugin files, run `claude plugin validate /path/to/obee/plugins/claude-code`. The Go test `plugin_test.go` in `cmd/obee` checks the same files against the real obee command tree.

## The five actions

| Action | Slash command | obee command |
| --- | --- | --- |
| Queue an item | `/obee:queue <project> <id> <title>` | `obee item add <project> <id> --title '<title>' --body-file <file>` |
| Start or continue a run | `/obee:run <item> [project] [--workflow <file>]` | `obee run <item> [project]` |
| Show status | `/obee:status` | `obee status` |
| Answer a wait | `/obee:answer <item> <text>` | `obee answer <item> '<text>'` |
| Cancel a run | `/obee:cancel <item>` | `obee cancel <item>` |

The title of an item and the text of an answer go to obee as one single-quoted argument, with each `'` written as `'\''`. Item ids and flags pass as they are.

If the last run failed, `obee run` refuses, and `/obee:run` offers `obee resume <item>` and then `obee run <item>`. After a cancelled run, `obee run` does not refuse: it starts a new run that repeats the finished steps. `/obee:run` and `/obee:cancel` ask the user to choose between resume and a new run.

If another `obee run` owns the state folder, the command runs `/obee:status` and waits. It does not retry. After `/obee:answer`, the status line keeps the question until the run continues.

Only the user starts an action: each of the five sets `disable-model-invocation: true`, so the model cannot load one by itself and run a pre-approved obee command, such as a cancel, without a prompt. The guide skill `obee` stays open to the model.

If an argument is missing, the command asks the user for it. The command never guesses an item id, a project or an answer.

## Layout

The layout follows the Claude Code plugin reference (`https://code.claude.com/docs/en/plugins/manifest-reference`).

- `.claude-plugin/plugin.json` is the manifest. Only `name` is required. The plugin also sets `version`, `description` and `author`.
- `.claude-plugin/marketplace.json` lets `claude plugin install` find the plugin in this folder.
- `skills/<action>/SKILL.md` is one slash command for each action. The reference says: "Prefer `skills/` for new plugins", and the command name is `/<plugin>:<directory>`, so `skills/queue/SKILL.md` is `/obee:queue`.
- `logo.svg` is the `icon` of the manifest. The reference says Claude Code does not read it; the directory listing does.
- `skills/obee/SKILL.md` tells the agent when to use the commands and that every action goes through the obee command line.

Codex and Copilot get the same shape later: a thin layer that runs the same obee commands.
