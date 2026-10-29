# A workflow for a project with no tracker and no forge: the items are in local.db, and the result is a branch in the source repository.
# Add an item with `obee item add`, then run it with `obee run <item> --workflow workflow.hcl`.

workflow "local" {}

# Every agent gets the same item object. A local names it once and holds no value: each read builds it again.
locals {
  item_input = { id = item.id, title = item.title, body = item.body }
}

agent "plan" {
  prompt = "prompts/plan.md"
  inputs = { item = local.item_input }
}

agent "implement" {
  prompt = "prompts/implement.md"
  inputs = { item = local.item_input, plan = agent.plan.text }
}

# A pass is one gate, one review and, when the head is not approved, one fix. The loop stops at the first approved head.
loop "review_fix" {
  max   = 3
  until = local.approved

  locals {
    approved = gate.check.passed && agent.review.verdict == "approve"
  }

  # The gate command is the one in the project file.
  gate "check" {}

  agent "review" {
    prompt  = "prompts/review.md"
    when    = gate.check.passed
    pin     = gate.check.head
    outputs = ["verdict"]
    inputs  = { item = local.item_input }
  }

  agent "fix" {
    prompt = "prompts/fix.md"
    when   = !local.approved
    inputs = {
      item     = local.item_input
      findings = gate.check.passed ? agent.review.text : gate.check.report
    }
  }
}

# The branch goes into the source repository under the id of the item. An item that used all of its passes is not pushed.
# A command step gets no token. For a remote that needs one, add env = { GH_TOKEN = env.GH_TOKEN }.
command "push" {
  run  = ["git", "push", "origin", "HEAD:refs/heads/${item.id}"]
  when = !loop.review_fix.exhausted
}
