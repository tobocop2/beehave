#!/bin/sh
# The stand-in for gotestsum. Usage: gotestsum --junitfile FILE -- ARGS. It runs the stand-in for go test and writes a JUnit file for the result.
file=$2
shift 3
mkdir -p "$(dirname "$file")"
if go test "$@"; then
  printf '<testsuites><testsuite name="all" tests="1" failures="0"><testcase name="all"/></testsuite></testsuites>\n' > "$file"
else
  printf '<testsuites><testsuite name="all" tests="1" failures="1"><testcase name="all"><failure message="the tests fail"/></testcase></testsuite></testsuites>\n' > "$file"
  exit 1
fi
