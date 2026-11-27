import { spawnSync } from "node:child_process"
import { writeFileSync } from "node:fs"

const GUARD = __GUARD_COMMAND__
const LOADED = __LOADED_PATH__

// The marker proves to beehest that this plugin loaded. A plugin that fails to write it does not load.
const markLoaded = () => writeFileSync(LOADED, "loaded\n")

// verdict returns why the call is refused, or "" when the guard command lets it through. Any failure to decide is a refusal.
const verdict = (directory, tool, args) => {
  try {
    const event = JSON.stringify({ directory, tool, args })
    const run = spawnSync(GUARD, { shell: true, input: event, encoding: "utf8", timeout: 30000 })
    if (run.error) return "the check of the command did not run"
    if (run.status === 2) return (run.stderr || "").trim() || "the command is not allowed"
    if (run.status !== 0) return "the check of the command failed"
    return ""
  } catch (err) {
    return String(err && err.message ? err.message : err) || "the check of the command threw"
  }
}

// refuse throws when the guard command refuses a call of the shell tool, which OpenCode 1.x names bash and OpenCode 2.x names shell.
const refuse = (directory, tool, args) => {
  if (tool !== "bash" && tool !== "shell") return
  const why = verdict(directory, tool, args)
  if (why !== "") throw new Error("beehest refused: " + why)
}

export default {
  id: "beehest-guard",
  // OpenCode 1.x calls server and refuses the tool call when the hook that it returns throws.
  server: async ({ directory }) => {
    markLoaded()
    return {
      "tool.execute.before": async (input, output) => refuse(directory, input.tool, output && output.args),
    }
  },
  // OpenCode 2.x calls setup and refuses the tool call when the hook throws. OpenCode 1.x calls setup too, with a context that has no tool.
  setup: async (ctx) => {
    if (!ctx.tool) return
    ctx.tool.hook("execute.before", async (input) => refuse(ctx.location.directory, input.tool, input.input))
    markLoaded()
  },
}
