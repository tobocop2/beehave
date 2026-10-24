---
type: Reference
title: Check
description: Run the full obee gate and report the result
x-ai-rulez:
  kind: command
  id: check
  metadata:
    priority: high
    usage: /check
---

# Check

1. Run `make check`.
2. If it fails, name the first failing step and its root cause before you propose a fix.
3. Do not lower a threshold, skip a test or add a `//nolint` to pass.
