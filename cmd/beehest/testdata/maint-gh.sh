#!/bin/sh
# The stand-in for gh. It logs every call to gh.log in the work folder. The state of the forge is in the folder gh of the work folder:
# gh/open holds one pull request number for each line (the open bump pull requests), gh/created holds one line for each pull request that was created,
# gh/state-<number> holds OPEN or MERGED for the pull request of that number.
# The owner login of the engine: beehest reads this token for an https remote. It is not a call of the forge, so it is not logged.
if [ "$1 $2" = "auth token" ]; then
  echo fake-gh-token
  exit 0
fi
echo "gh $*" >> "@WORK@/gh.log"
mkdir -p "@WORK@/gh"
case "$1 $2" in
  "pr list")
    case "$*" in
      *--head*--jq*)
        branch=$(printf '%s\n' "$*" | sed 's/.*--head \([^ ]*\).*/\1/')
        sed -n "s/^$(printf '%s' "$branch" | sed 's/\//\\\//g') //p" "@WORK@/gh/created" 2> /dev/null | head -n 1
        ;;
      *--head*) echo '[]' ;; # the engine asks for the pull request of an item branch: there is none
      *)
        # Like gh: the open pull requests as JSON, then the --jq program of the call. gh/open holds <number><TAB><branch> for each open pull request.
        filter=.
        while [ $# -gt 0 ]; do
          if [ "$1" = "--jq" ]; then filter=$2; fi
          shift
        done
        # With no open pull request, gh prints the empty list [] for --json. The --jq programs of the scripts print nothing for it.
        if [ ! -f "@WORK@/gh/open" ]; then
          if [ "$filter" = . ]; then echo '[]'; fi
          exit 0
        fi
        jq -Rn '[inputs | split("\t") | {number: (.[0] | tonumber), headRefName: .[1]}]' "@WORK@/gh/open" | jq -r "$filter"
        ;;
    esac
    exit 0
    ;;
  "pr create")
    branch=$(printf '%s\n' "$*" | sed 's/.*--head \([^ ]*\).*/\1/')
    n=$(( $(wc -l < "@WORK@/gh/created" 2> /dev/null || echo 0) + 1 ))
    url="https://github.example/acme/pull/$n"
    printf '%s %s\n' "$branch" "$url" >> "@WORK@/gh/created"
    echo "$url"
    exit 0
    ;;
  "pr view")
    number=${3##*/}
    if [ -f "@WORK@/gh/state-$number" ]; then cat "@WORK@/gh/state-$number"; else echo OPEN; fi
    exit 0
    ;;
  "issue comment")
    cat > /dev/null
    exit 0
    ;;
  "api graphql")
    # The read of who wrote an issue or a pull request, in the shape of gh: every item of the maintenance scripts is the owner's own.
    case "$*" in
      *issueOrPullRequest*) echo '{"data":{"repository":{"issueOrPullRequest":{"authorAssociation":"OWNER"}}}}' ;;
    esac
    exit 0
    ;;
esac
exit 0
