import { spawnSync } from "node:child_process"
import { writeFileSync } from "node:fs"

const GUARD = __GUARD_COMMAND__
const LOADED = __LOADED_PATH__

// verdict returns why the call is refused, or "" when the guard command lets it through. Any failure to decide is a refusal.
const verdict = (directory, command) => {
  try {
    const event = JSON.stringify({ cwd: directory, tool_name: "Bash", tool_input: { command } })
    const run = spawnSync(GUARD, { shell: true, input: event, encoding: "utf8", timeout: 30000 })
    if (run.error) return "the check of the command did not run"
    if (run.status === 2) return (run.stderr || "").trim() || "the command is not allowed"
    if (run.status !== 0) return "the check of the command failed"
    return ""
  } catch (err) {
    return String(err && err.message ? err.message : err) || "the check of the command threw"
  }
}

export const BeehaveGuard = async ({ directory }) => {
  // The marker proves to beehave that this plugin loaded. A plugin that fails to write it does not load.
  writeFileSync(LOADED, "loaded\n")

  return {
    // OpenCode refuses the tool call when this hook throws.
    "tool.execute.before": async (input, output) => {
      if (input.tool !== "bash") return
      const why = verdict(directory, output && output.args ? output.args.command : undefined)
      if (why !== "") throw new Error("beehave refused: " + why)
    },
  }
}
