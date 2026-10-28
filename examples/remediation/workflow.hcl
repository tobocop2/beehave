# The remediation workflow: plan, implement, then review and fix until the gate passes and the review approves.
# Every method value is in this file or in a prompt next to it. Go holds none of it.

workflow "remediation" {
  method_version = "v0"
  require_model  = true
}

agent "plan" {
  prompt = "prompts/plan.md"
  model  = "opus"
}

agent "implement" {
  prompt         = "prompts/implement.md"
  model          = "sonnet"
  require_skills = ["blast-radius-review"]
  inputs         = { plan = agent.plan.text }
}

loop "review_fix" {
  max   = 5
  until = gate.check.passed && agent.review.verdict == "approve"

  # The gate command is the one of the project.
  gate "check" {}

  # No resume_from: the reviewer starts fresh in each round and judges the commit the gate ran on.
  agent "review" {
    prompt  = "prompts/review.md"
    model   = "opus"
    when    = gate.check.passed
    pin     = gate.check.head
    outputs = ["verdict"]
  }

  # Rounds 1 to 3 resume the implementer. Rounds 4 and 5 start fresh, one tier up.
  agent "fix" {
    prompt      = "prompts/fix.md"
    model       = loop.round <= 3 ? "sonnet" : "opus"
    resume_from = loop.round <= 3 ? agent.implement : null
    when        = !(gate.check.passed && agent.review.verdict == "approve")
    inputs      = { findings = gate.check.passed ? agent.review.text : gate.check.report, round = loop.round }
  }
}

# After the last round, a person rules on each open finding.
wait "adjudicate" {
  question = "The review loop used all of its rounds. Rule on each open finding: fix it now, or park it with the cost if it is wrong."
  when     = loop.review_fix.exhausted
}
