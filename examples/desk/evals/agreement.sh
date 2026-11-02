#!/bin/sh
# Computes Cohen's kappa of the people and of the judge. $1: intent. $2: eval run. Exit 0 done.
# The judge is trusted when its mean kappa against the people is at least the people's kappa.
# The last line is JSON: {"replies", "kappa_people", "kappa_judge", "judge_trusted"}.
set -eu
. "$(dirname "$0")/../gateway/lib.sh"
for f in "$DESK_HOME/evals/$2/calib"/*.json; do
    jq -r '[.id, .judge] | @tsv' "$f"
done | awk -F '\t' -v goldf="$DESK_HOME/gold/$1-replies.tsv" '
    function kappa(x, y,    i, agree, px, py, pe, po) {
        for (i = 1; i <= n; i++) { if (G[x, i] == G[y, i]) agree++; if (G[x, i] == "good") px++; if (G[y, i] == "good") py++ }
        po = agree / n; px /= n; py /= n
        pe = px * py + (1 - px) * (1 - py)
        return (pe == 1) ? 1 : (po - pe) / (1 - pe)
    }
    BEGIN { while ((getline l < goldf) > 0) { split(l, g, "\t"); a[g[1]] = g[4]; b[g[1]] = g[5] } }
    ($1 in a) { n++; G["a", n] = a[$1]; G["b", n] = b[$1]; G["j", n] = $2 }
    END {
        if (n == 0) { print "{\"replies\": 0, \"kappa_people\": 0, \"kappa_judge\": 0, \"judge_trusted\": false}"; exit }
        kp = kappa("a", "b"); kj = (kappa("j", "a") + kappa("j", "b")) / 2
        printf "{\"replies\": %d, \"kappa_people\": %.3f, \"kappa_judge\": %.3f, \"judge_trusted\": %s}\n", n, kp, kj, (kj >= kp ? "true" : "false")
    }'
