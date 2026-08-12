#!/usr/bin/env bash
# =============================================================================
# MANUAL live smoke test for the forms module.  RUN BY HAND.
# =============================================================================
#
# WHY THIS EXISTS
# ---------------
# The automated tests exercise the forms module against httptest servers and
# in-memory SQLite.  This script walks the real running backend end to end the
# way an embedding site would:
#
#   create form -> public definition -> wait out the time trap -> submit
#     -> submission listed as received -> lead created
#   then the spam side: a honeypot-filled submission gets the same success
#   response but is stored as spam and creates no second lead.
#
# The double-opt-in confirm flow is NOT exercised here: the confirmation link
# only exists inside the delivery email (or the redacted mailer log), so it
# cannot be scripted without scraping logs.  It is covered by the service and
# handler suites; confirm it by hand with a real SMTP_HOST when needed.
#
# REQUIREMENTS
# ------------
#   - the backend running (MySQL up, migrations applied at boot)
#   - curl and jq on PATH
#   - an ADMIN JWT (the script reads the caller's user id for the lead owner)
#
# USAGE
# -----
#   scripts/forms_live_smoke.sh -t "$JWT"                  # against localhost:8080
#   API_BASE=http://localhost:8090/api/v1 scripts/forms_live_smoke.sh -t "$JWT"
#
#   -t TOKEN   admin bearer token (or set FORMS_SMOKE_TOKEN)
#   -k         keep the created form (default: it is archived+deleted at the end)
#
# Every public request sends a foreign Origin header on purpose: the public
# forms endpoints must answer cross-origin, and a passing curl without Origin
# would prove nothing about the embedded path.
set -euo pipefail

API_BASE="${API_BASE:-http://localhost:8080/api/v1}"
TOKEN="${FORMS_SMOKE_TOKEN:-}"
KEEP=0
ORIGIN="https://smoke-test.example"

while getopts "t:k" opt; do
  case "$opt" in
    t) TOKEN="$OPTARG" ;;
    k) KEEP=1 ;;
    *) exit 2 ;;
  esac
done

[ -n "$TOKEN" ] || { echo "admin token required (-t or FORMS_SMOKE_TOKEN)" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

auth=(-H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json")
pub=(-H "Origin: $ORIGIN" -H "Content-Type: application/json")
stamp="$(date +%s)"
visitor="smoke-visitor-${stamp}@example.com"

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo; echo "== $*"; }

step "who am i"
me="$(curl -sf "${auth[@]}" "$API_BASE/users/me")" || fail "GET /users/me (is the token an admin JWT?)"
owner_id="$(jq -r '.data.id' <<<"$me")"
echo "user id: $owner_id"

step "create + publish form"
form="$(curl -sf "${auth[@]}" -X POST "$API_BASE/forms" -d @- <<JSON
{
  "name": "Smoke quote request ${stamp}",
  "status": "published",
  "submit_action": "message",
  "thank_you_message": "Thanks - we reply within one business day.",
  "consent_text": "I agree to be contacted about my request.",
  "create_lead": true,
  "default_owner_id": ${owner_id},
  "fields": [
    {"name": "first_name", "label": "First name", "type": "text", "required": true},
    {"name": "email", "label": "Email", "type": "email", "required": true},
    {"name": "budget", "label": "Budget", "type": "select", "required": true,
     "options": ["under 10k", "10k-50k", "50k plus"]},
    {"name": "message", "label": "Message", "type": "textarea", "required": false}
  ]
}
JSON
)" || fail "POST /forms"
form_id="$(jq -r '.data.id' <<<"$form")"
public_id="$(jq -r '.data.public_id' <<<"$form")"
echo "form $form_id public_id $public_id"

cleanup() {
  if [ "$KEEP" -eq 0 ]; then
    curl -sf "${auth[@]}" -X DELETE "$API_BASE/forms/$form_id" >/dev/null || true
    echo "(form $form_id deleted)"
  else
    echo "(form $form_id kept)"
  fi
}
trap cleanup EXIT

step "public definition (cross-origin)"
def="$(curl -sf "${pub[@]}" "$API_BASE/forms/public/$public_id")" || fail "GET public definition"
challenge="$(jq -r '.data.challenge' <<<"$def")"
honeypot="$(jq -r '.data.honeypot_field' <<<"$def")"
jq -r '.data | {name, fields: (.fields | length), honeypot_field}' <<<"$def"
[ -n "$challenge" ] && [ "$challenge" != "null" ] || fail "definition carries no challenge"

step "waiting out the time trap (4s)"
sleep 4

step "clean submission"
outcome="$(curl -sf "${pub[@]}" -X POST "$API_BASE/forms/public/$public_id/submissions" -d @- <<JSON
{
  "values": {
    "first_name": "Smoke",
    "email": "${visitor}",
    "budget": "10k-50k",
    "message": "Live smoke test submission."
  },
  "consent": true,
  "challenge": "${challenge}",
  "page_url": "${ORIGIN}/landing"
}
JSON
)" || fail "POST submission"
jq -r '.data' <<<"$outcome"
[ "$(jq -r '.data.action' <<<"$outcome")" = "message" ] || fail "unexpected outcome action"

step "submission recorded as received"
subs="$(curl -sf "${auth[@]}" "$API_BASE/forms/$form_id/submissions")" || fail "list submissions"
status="$(jq -r --arg e "$visitor" '.data[] | select(.email == $e) | .status' <<<"$subs")"
lead_id="$(jq -r --arg e "$visitor" '.data[] | select(.email == $e) | .lead_id' <<<"$subs")"
echo "status=$status lead_id=$lead_id"
[ "$status" = "received" ] || fail "expected status received, got '$status'"
[ "$lead_id" != "null" ] && [ -n "$lead_id" ] || fail "no lead was created"

step "lead exists"
lead="$(curl -sf "${auth[@]}" "$API_BASE/leads/$lead_id")" || fail "GET lead $lead_id"
jq -r '.data | {id, first_name, email, source, status}' <<<"$lead"

step "honeypot submission is swallowed"
def2="$(curl -sf "${pub[@]}" "$API_BASE/forms/public/$public_id")"
challenge2="$(jq -r '.data.challenge' <<<"$def2")"
sleep 4
spam="$(curl -sf "${pub[@]}" -X POST "$API_BASE/forms/public/$public_id/submissions" -d @- <<JSON
{
  "values": {
    "first_name": "Bot",
    "email": "bot-${stamp}@example.com",
    "budget": "under 10k"
  },
  "consent": true,
  "challenge": "${challenge2}",
  "${honeypot}": "https://spam.example",
  "page_url": "${ORIGIN}/landing"
}
JSON
)" || fail "honeypot submission was rejected outright (it must look accepted)"
[ "$(jq -r '.success' <<<"$spam")" = "true" ] || fail "honeypot response is not success-shaped"
subs2="$(curl -sf "${auth[@]}" "$API_BASE/forms/$form_id/submissions?status=spam")"
spam_reason="$(jq -r --arg e "bot-${stamp}@example.com" '.data[] | select(.email == $e) | .spam_reason' <<<"$subs2")"
echo "spam_reason=$spam_reason"
[ "$spam_reason" = "honeypot" ] || fail "expected a spam row with reason honeypot"

echo
echo "ALL CHECKS PASSED"
