#!/bin/sh
# The stand-in for systemd-run, so that a step with max_new_processes runs on a host with no systemd user manager.
# It uses shell builtins only: the scripts have a PATH with only the folder fake, so no program of PATH is found.
# It logs the arguments of each call to systemd-run.log in the work folder, one line for each call, and sets no limit.
# The probe of the runner starts the command true: the stand-in answers it without a program. Any other command has a path.
echo "systemd-run $*" >> "@WORK@/systemd-run.log"
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
[ "$#" -gt 1 ] || exit 2
shift
[ "$1" = "true" ] && exit 0
exec "$@"
