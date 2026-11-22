# The project file of the research desk. Copy it to <config folder>/projects/research-desk.star.
# The project has no source: the desk changes no code and has no git repository. The quote check and the prose check are program gates.
# The agent steps only read the case that the workflow passes and reach no host, so each runs in the sandbox with no network.

# The sandbox of the gates is off because the staleness gate fetches every cited page again. A sandboxed gate reaches only the gate_network hosts, which a research question cannot name in advance.

project(sandbox = "off")
