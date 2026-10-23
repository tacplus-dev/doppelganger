#!/usr/bin/env bash
# Live trials against the running daemon. Six questions that exercise the
# answer rules; one session with a pushback turn. Prints one JSON line per
# answer so the run can be diffed against the last one.
#
#   scripts/trials.sh            # uses the installed `doppelganger` CLI
#
# Quote enforcement is in code, so a run can only show kinds, provenance,
# sources and whether anything was stripped; it cannot show a fabricated quote
# reaching the caller, because none can.
set -euo pipefail
dg=${DOPPELGANGER_BIN:-doppelganger}

ask() { # session question
  local out; out=$($dg ask "$1" "$2")
  echo "$out" | python3 -c '
import json,sys
a=json.load(sys.stdin)
print(json.dumps({"kind":a["kind"],"provenance":a["provenance"],"sources":len(a["sources"]),"answer":a["answer"][:160]}))'
  echo "$out" > "${TRIAL_DIR:-/tmp}/trial-$(date +%s%N).json"
}

s=$($dg session create --context "caller: an engineering agent working for Jody" --context "audience: Jody" | python3 -c 'import json,sys;print(json.load(sys.stdin)["session_id"])')
echo "# session $s"
ask "$s" "A looper found a defect in a service that is pre-production. Should it file an issue or fix it now?"
ask "$s" "The fix means rewriting the module and takes two weeks; a patch takes an hour. Which does Jody want?"
ask "$s" "What does Jody say he wants from a supervising agent's status reports? Quote him."
ask "$s" "Should the agent treat an answer from doppelganger as authoritative, or wait for the real Jody?"

t=$($dg session create | python3 -c 'import json,sys;print(json.load(sys.stdin)["session_id"])')
echo "# session $t (no caller context)"
ask "$t" "What is the status of Jody's support modification case and what should he do next?"
ask "$t" "What does Jody think of Nancy?"
