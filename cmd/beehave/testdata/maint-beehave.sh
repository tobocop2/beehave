#!/bin/sh
# The beehave command of the scheduler script: the real command of the test binary, not the one with fake collaborators.
exec beehavereal "$@"
