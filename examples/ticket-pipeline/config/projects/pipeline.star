# Copy this file to <config folder>/projects/pipeline.star.
# The project has no source and no repository: the workflows change no code, and every script runs by its absolute path (PIPE_DIR).
# The tracker is the local one: a chunk of days is an item, and no forge is needed.
# The sandbox is off because the classify step reads two files outside its work folder, in PIPE_HOME and PIPE_DIR. A project file names fixed paths only, and the Read tool of a sandboxed step cannot read outside its working folders.
project(sandbox = "off")
