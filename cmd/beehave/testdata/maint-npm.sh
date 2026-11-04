#!/bin/sh
# The stand-in for npm. It logs every call to tools.log in the work folder.
# outdated: prints the file fixtures/npm.json and exits 1 when the file holds text, as npm does when something is outdated.
# update: writes package-lock.json, a file that is new in the repository.
echo "npm $*" >> "@WORK@/tools.log"
case "$1" in
  outdated)
    if [ -s "@WORK@/fixtures/npm.json" ]; then cat "@WORK@/fixtures/npm.json"; exit 1; fi
    ;;
  test)
    mkdir -p "${JEST_JUNIT_OUTPUT_DIR:-reports}"
    printf '<testsuites><testsuite name="all" tests="1" failures="0"><testcase name="all"/></testsuite></testsuites>\n' > "${JEST_JUNIT_OUTPUT_DIR:-reports}/junit.xml"
    ;;
  update)
    printf '{"lockfileVersion": 3, "left-pad": "1.3.0"}\n' > package-lock.json
    ;;
esac
exit 0
