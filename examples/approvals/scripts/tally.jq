# The state of one request. The input is the array of rows that tally.sh read in one query: the rows of the request and the rows for
# everybody ("-"). Each row has "seen", the seq of the last change. Arguments:
#   $req     the request id
#   $now     the clock, in epoch seconds
# The seq, the current version (the last version row of the document) and the rows all come from that one read, so no order of reads can
# split them. A request that has no opened row is an error.
# The ledger rows are facts. This program decides what they mean now: which sign-offs count, which roles are satisfied
# and the status. Nothing here is stored, so a change to the document or to the clock changes the answer with no write.
def uniq: reduce .[] as $x ([]; if index($x) == null then . + [$x] else . end);

(.[0].seen // 0) as $seq
| (map(select(.type == "opened" and .req == $req)) | first) as $o
| (map(select(.type == "version" and .doc == $o.doc)) | last | .hash // "") as $hash
| map(del(.seen)) as $ev
| if $o == null then error("no such request") else
  ($o.detail | fromjson) as $d
  | $d.policy as $p
  | ($ev | map(select(.type == "closed" and .req == $req)) | last) as $closed
  | ($ev | map(select(.type == "extended" and .req == $req))) as $exts
  | (([$d.deadline] + ($exts | map(.detail | fromjson | .deadline))) | max) as $deadline
  | ($ev | map(select(.type == "delegate"))) as $delegations
  | def members($r): $p.roles[$r].members // [];
    # A delegation is in force at time $t when a member of the role gave it for this role and $t is inside its window.
    def given($r; $t): $delegations | map(. as $g | select($g.role == $r and $g.at <= $t and (($g.detail | fromjson).until >= $t) and (members($r) | index($g.who)) != null));
    def delegated($w; $r; $t): given($r; $t) | any((.detail | fromjson).to == $w);
    def standing($s): ($p.roles[$s.role] != null)
        and (($s.who == $d.author) | not)
        and ((members($s.role) | index($s.who)) != null or delegated($s.who; $s.role; $s.at));
    ($ev | map(select(.type == "signoff" and .req == $req)) | sort_by(.seq)) as $all
  | ($all | map(select(.hash == $hash))) as $cur
  | ($cur | map(select(standing(.)))) as $eligible
  # A person counts for one role: the first role in which they signed. Within that role the last decision wins.
  | ($eligible | map(.who) | uniq) as $people
  | ($people | map(. as $w | ($eligible | map(select(.who == $w))) as $mine
        | ($mine | first | .role) as $r
        | ($mine | map(select(.role == $r)) | last)
        | {who, role, decision, reason: ((.detail | fromjson).reason // "")})) as $final
  | ($cur | map(select(standing(.) | not))
        | map({who, role, reason: (if .who == $d.author then "author" elif $p.roles[.role] == null then "unknown-role" else "not-in-role" end)})) as $ignored_a
  | ($people | map(. as $w | ($eligible | map(select(.who == $w))) as $mine | ($mine | first | .role) as $r
        | $mine | map(select(.role != $r)) | map({who, role, reason: "second-role"})) | add // []) as $ignored_b
  | ($p.roles | to_entries | map(. as $e
        | ($final | map(select(.role == $e.key and .decision == "approve") | .who)) as $ap
        | ($final | map(select(.role == $e.key and .decision == "reject"))) as $rj
        | {key: $e.key, value: {need: $e.value.need, required: $e.value.required, approved: $ap, rejected: $rj,
              people: (members($e.key) + (given($e.key; $now) | map((.detail | fromjson).to)) | uniq),
              ok: (($ap | length) >= $e.value.need)}}) | from_entries) as $roles
  | ($roles | to_entries) as $rs
  | ($rs | map(select(.value.ok) | .key)) as $okroles
  | ($rs | map(select(.value.ok | not) | .key)) as $missing
  | ($rs | map(select(.value.required and (.value.rejected | length) > 0)) | length) as $vetoes
  | (($rs | all((.value.required | not) or .value.ok)) and (($okroles | length) >= $p.min_roles)) as $quorum
  # Sign-offs of an earlier version, by people who have not signed this one: the people whose approval a change reset.
  | ($all | map(select(.hash != $hash and .decision == "approve" and standing(.)))
        | map(select(. as $s | ($final | any(.who == $s.who)) | not))) as $old
  | ($old | unique_by(.who)) as $stale
  | {req: $req, doc: $o.doc, kind: $d.kind, author: $d.author, hash: $hash, seq: $seq, now: $now, opened: $o.at,
     deadline: $deadline, left: ($deadline - $now), pct: ((($now - $o.at) * 100) / ($deadline - $o.at) | floor),
     extensions: ($exts | length), escalate_to: $p.escalate_to, min_roles: $p.min_roles, satisfied: ($okroles | length),
     roles: $roles, missing: $missing, rejections: ($final | map(select(.decision == "reject"))),
     stale: ($stale | map({who, role, hash})), stale_from: ($old | first | .hash // ""), stale_roles: ($stale | map(.role) | uniq),
     ignored: ($ignored_a + $ignored_b),
     status: (if $closed != null then "closed" elif $vetoes > 0 then "rejected" elif $quorum then "approved"
              elif $now > $deadline then "expired" else "pending" end),
     outcome: ($closed.decision // "")}
end
