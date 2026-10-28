# The remediation workflow: plan, implement, then review and fix until the gate passes and the review approves.
# Every method value is in this file or in a prompt next to it. Go holds none of it.

workflow "remediation" {
  method_version = "v0"
  require_model  = true
}

agent "plan" {
  prompt = "prompts/plan.md"
  model  = "opus"
  inputs = { item = { id = item.id, title = item.title, body = item.body } }
}

agent "implement" {
  prompt         = "prompts/implement.md"
  model          = "sonnet"
  require_skills = ["blast-radius-review"]

  # A command of these kinds in the transcript is recorded in agent.implement.violations. The step does not fail.
  # The loop below reads the violations of the latest run of a lane: a hit sends the run back to a fix round.
  forbid_commands = ["bulk-staging", "local-delete", "remote-delete", "opaque-command"]
  inputs          = { item = { id = item.id, title = item.title, body = item.body }, plan = agent.plan.text }
}

# A round is one fix and one review of it. The loop starts with a review, so five fix rounds need six passes: the sixth reviews the fifth fix.
# The violations of the latest lane run are agent.implement.violations in round 1 and, after that, the violations of the fix of the round before:
#   loop.round == 1 ? agent.implement.violations : loop.previous.agent.fix.violations
# Call this expression L. It appears five times below: in until, in the review inputs, in the when of fix and twice in the findings of fix. Each copy must match L exactly.
# The language has no local value, so a change to L needs an edit of every copy. A follow-up item records L once.
# The review reads them. A round with violations does not end the loop, and its fix gets them as findings ahead of the review text.
loop "review_fix" {
  max   = 6
  until = gate.check.passed && agent.review.verdict == "approve" && (loop.round == 1 ? agent.implement.violations : loop.previous.agent.fix.violations) == ""

  # The gate command is the one of the project.
  gate "check" {}

  # No resume_from: the reviewer starts fresh in each round and judges the commit the gate ran on.
  agent "review" {
    prompt  = "prompts/review.md"
    model   = "opus"
    when    = gate.check.passed
    pin     = gate.check.head
    outputs = ["verdict"]

    # A review that writes no verdict file is asked once more.
    output_tries = 2
    inputs       = { item = { id = item.id, title = item.title, body = item.body }, violations = loop.round == 1 ? agent.implement.violations : loop.previous.agent.fix.violations }
  }

  # Rounds 1 to 3 resume the implementer. Rounds 4 and 5 start fresh, one tier up.
  agent "fix" {
    prompt      = "prompts/fix.md"
    model       = loop.round <= 3 ? "sonnet" : "opus"
    resume_from = loop.round <= 3 ? agent.implement : null
    when        = loop.round <= 5 && !(gate.check.passed && agent.review.verdict == "approve" && (loop.round == 1 ? agent.implement.violations : loop.previous.agent.fix.violations) == "")

    # The violations of this fix are recorded. The review of the next round reads them as loop.previous.agent.fix.violations.
    forbid_commands = ["bulk-staging", "local-delete", "remote-delete", "opaque-command"]
    inputs = {
      item  = { id = item.id, title = item.title, body = item.body }
      round = loop.round

      # The violations come first, one for each line, then the review text or the gate report.
      findings = (loop.round == 1 ? agent.implement.violations : loop.previous.agent.fix.violations) == "" ? (gate.check.passed ? agent.review.text : gate.check.report) : "${loop.round == 1 ? agent.implement.violations : loop.previous.agent.fix.violations}\n\n${gate.check.passed ? agent.review.text : gate.check.report}"
    }
  }
}

# After the last round, a person rules on each open finding.
wait "adjudicate" {
  question = "The review loop used all of its rounds. Rule on each open finding: fix it now, or park it with the cost if it is wrong."
  when     = loop.review_fix.exhausted
}

# The ruling of the owner is applied once. No review follows it.
agent "settle" {
  prompt = "prompts/settle.md"
  model  = "sonnet"
  when   = loop.review_fix.exhausted
  inputs = { ruling = wait.adjudicate.answer }
}

# An approved head is marked ready and then left to the owner: this step does not set `merge`, so it never merges.
# If the loop is exhausted, the run ends at `settle` and this step does not run. The owner then marks the pull request ready by hand.
merge "ship" {
  head = gate.check.head
  when = !loop.review_fix.exhausted
}
