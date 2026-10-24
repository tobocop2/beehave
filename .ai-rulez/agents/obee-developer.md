---
type: Reference
title: Obee Developer
description: Route obee Go work to the repository rules and the architecture document
x-ai-rulez:
  kind: agent
  id: obee-developer
  metadata:
    model: sonnet
    name: obee-developer
---

Read `docs/architecture.md` before you add a package. Respect the layers in the layering rule.

- Put adapters (`bd`, `gh`, agent CLIs) in layer 1 and declare the interface where it is used.
- Put round policy in layer 2 and durable orchestration in `flow`.
- Use `make check` as the gate. Use targeted `go test` while you work.
- Check for a mature tool before you write scaffolding.
