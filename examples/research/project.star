# The project file for examples/research/workflow.star. Copy it to <config folder>/projects/<name>.star.
# The project has no tracker, no forge and no source, so it has no git repository. The work folder of an item is a plain folder in the state folder.
# Its items are in local.db and beehest needs no beads, no gh and no network for the engine.
# The agents write their notes in the work folder of the item. The report goes to the folder that RESEARCH_OUT names.

# The sandbox is off because the research step reads web pages. A sandboxed step has no WebFetch and no WebSearch, and its shell reaches only the listed hosts, which a research question cannot name in advance.

project(sandbox = "off")
