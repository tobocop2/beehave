#!/bin/sh
# Prints the cost-weighted confusion matrix of an eval run. $1: intent. $2: eval run. Exit 0 done.
# Truth is the first person's answer. A right kind with a wrong amount is "<kind>-wrong-amount".
# $DESK_HOME/gold/costs.tsv prices outcomes (truth, predicted, cost). An unlisted outcome costs 1 when wrong, 0 when right.
# The last line is JSON: {"cases", "accuracy", "mean_cost", "people_agree"}.
set -eu
. "$(dirname "$0")/../gateway/lib.sh"
gold="$DESK_HOME/gold"
runs="$DESK_HOME/evals/$2/cases"
for f in "$runs"/*.json; do
    jq -r '[.id, (if .decision == "propose" then .kind elif .decision == "decline" or .decision == "escalate" then "person" else "none" end), .amount] | @tsv' "$f"
done | awk -F '\t' -v costs="$gold/costs.tsv" -v goldf="$gold/$1.tsv" '
    BEGIN {
        while ((getline l < costs) > 0) { split(l, c, "\t"); price[c[1] SUBSEP c[2]] = c[3] }
        while ((getline l < goldf) > 0) { split(l, g, "\t"); ka[g[1]] = g[3]; aa[g[1]] = g[4]; kb[g[1]] = g[5]; ab[g[1]] = g[6] }
    }
    ($1 in ka) {
        t = ka[$1]; p = $2
        if (p == t && $3 != aa[$1] && aa[$1] != "") p = t "-wrong-amount"
        n++; cell[t SUBSEP p]++; truths[t] = 1; preds[p] = 1
        if (p == t) right++
        cost += ((t SUBSEP p) in price) ? price[t SUBSEP p] : (p == t ? 0 : 1)
        if (ka[$1] == kb[$1] && aa[$1] == ab[$1]) same++
    }
    END {
        if (n == 0) { print "{\"cases\": 0, \"accuracy\": 0, \"mean_cost\": 999, \"people_agree\": 0}"; exit }
        printf "%-14s", "truth \\ clerk"; for (p in preds) printf " %14s", p; printf "\n"
        for (t in truths) { printf "%-14s", t; for (p in preds) printf " %14d", cell[t SUBSEP p]; printf "\n" }
        printf "{\"cases\": %d, \"accuracy\": %.3f, \"mean_cost\": %.3f, \"people_agree\": %.3f}\n", n, right / n, cost / n, same / n
    }'
