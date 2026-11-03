#!/bin/sh
# The stand-in for go. The repository is named by the module line of go.mod. It logs every call to tools.log in the work folder.
# list: prints the file fixtures/go-<repo>.json, which holds the output of `go list -u -m -json all`.
# get: raises every require line from v1.2.0 to v1.3.0 and writes go.sum, a file that is new in the repository.
# mod: does nothing.
# test: takes the first line of the file gate-results when it exists. The line red fails the test; any other line passes.
echo "go $*" >> "@WORK@/tools.log"
module=$(sed -n 's/^module //p' go.mod)
repo=${module##*/}
case "$1" in
  list)
    [ -f "@WORK@/fixtures/go-$repo.json" ] && cat "@WORK@/fixtures/go-$repo.json"
    exit 0
    ;;
  get)
    sed 's/ v1\.2\.0/ v1.3.0/' go.mod > go.mod.new
    mv go.mod.new go.mod
    printf 'example.com/lib v1.3.0 h1:e2e\n' > go.sum
    ;;
  test)
    if [ -f "@WORK@/gate-results" ]; then
      first=$(head -n 1 "@WORK@/gate-results")
      tail -n +2 "@WORK@/gate-results" > "@WORK@/gate-results.new"
      mv "@WORK@/gate-results.new" "@WORK@/gate-results"
      if [ "$first" = red ]; then echo "FAIL: the tests of $repo fail after the bump"; exit 1; fi
    fi
    ;;
esac
exit 0
