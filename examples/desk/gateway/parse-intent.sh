#!/bin/sh
# Parses the intent text of a clerk. stdin: the text. Exit 0 and the intent as one line of JSON when the text is exactly one JSON object.
# Exit 1 for an empty text, text that is not JSON, or JSON that is not an object. The clerk can send any text, so the desk never decodes it in the workflow.
set -eu
jq -ces 'if length == 1 and (.[0] | type) == "object" then .[0] else error("not one JSON object") end' 2> /dev/null || exit 1
