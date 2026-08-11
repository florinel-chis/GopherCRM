#!/usr/bin/env bash
# =============================================================================
# MANUAL live-provider smoke test for the AEO module.  RUN BY HAND.  COSTS MONEY.
# =============================================================================
#
# WHY THIS EXISTS
# ---------------
# Every automated test in this repository talks to an httptest server or an
# in-memory SQLite database.  That proves the wiring, not that a real answer
# engine accepts our request shape, that its credentials work, or that the
# citation fields we read are the ones it actually sends.  This script is the
# only thing that exercises the live path, so it is deliberately kept out of CI:
# it spends real API credit and its results are non-deterministic.
#
# It walks the module end to end against a running backend:
#   providers -> profile -> prompt -> run -> poll -> answers -> dashboard
#                                                             -> citations
# and prints what each step returned.  Nothing is asserted beyond HTTP status —
# LLM answers vary, and a script that failed on wording would cry wolf daily.
#
# COST
# ----
# One run is (active prompts x configured engines) calls.  Start from an empty
# or near-empty prompt list.  With every hosted key set and 100 prompts tracked
# this is 600 calls; the script prints the projected number and, unless -y is
# given, asks before starting the run.
#
# REQUIREMENTS
# ------------
#   - the backend running with at least one provider key in its environment
#   - curl and jq on PATH
#   - an admin (or sales) JWT: the run and prompt endpoints are write-guarded
#
# USAGE
# -----
#   scripts/aeo_live_smoke.sh -t "$JWT"                    # against localhost:8080
#   API_BASE=http://localhost:8090/api/v1 scripts/aeo_live_smoke.sh -t "$JWT"
#   scripts/aeo_live_smoke.sh -t "$JWT" -p "Which CRM is best for startups?"
#   scripts/aeo_live_smoke.sh -t "$JWT" -n                 # no run: read-only checks
#
#   -t TOKEN   admin/sales bearer token (or set AEO_SMOKE_TOKEN)
#   -p TEXT    prompt to create and query (default: a generic CRM question)
#   -o ORIGIN  send this Origin header on every request (default: none)
#   -n         skip the run — only exercise the read paths
#   -y         do not ask for confirmation before spending provider credit
#   -k         keep the created prompt (default: it is deleted at the end)
#
# The -o flag matters more than it looks: browsers send Origin on every API call
# and the CORS middleware rejects unlisted origins bare and unlogged, which
# presents in the UI as a login failure while curl without the header works.
# Pass the UI origin here when smoke-testing a deployment.
# =============================================================================

set -euo pipefail

API_BASE="${API_BASE:-http://localhost:8080/api/v1}"
TOKEN="${AEO_SMOKE_TOKEN:-}"
PROMPT_TEXT="Which CRM is best for a small sales team?"
ORIGIN=""
DO_RUN=1
ASSUME_YES=0
KEEP_PROMPT=0
POLL_ATTEMPTS="${AEO_SMOKE_POLL_ATTEMPTS:-60}"
POLL_INTERVAL="${AEO_SMOKE_POLL_INTERVAL:-5}"

while getopts ":t:p:o:nyk" opt; do
	case "$opt" in
		t) TOKEN="$OPTARG" ;;
		p) PROMPT_TEXT="$OPTARG" ;;
		o) ORIGIN="$OPTARG" ;;
		n) DO_RUN=0 ;;
		y) ASSUME_YES=1 ;;
		k) KEEP_PROMPT=1 ;;
		*) echo "unknown option -$OPTARG; see the header of this script" >&2; exit 2 ;;
	esac
done

for tool in curl jq; do
	command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required" >&2; exit 2; }
done

if [ -z "$TOKEN" ]; then
	echo "no token: pass -t or set AEO_SMOKE_TOKEN (admin or sales)" >&2
	exit 2
fi

step() { printf '\n=== %s\n' "$*"; }
fail() { printf '!!! %s\n' "$*" >&2; exit 1; }

# api METHOD PATH [BODY] — prints the response body, fails on an unexpected status.
# The status is appended on its own line by curl and split off here so the body
# stays valid JSON for jq.
api() {
	local method="$1" path="$2" body="${3:-}"
	local args=(-sS -X "$method" -w '\n%{http_code}'
		-H "Authorization: Bearer $TOKEN"
		-H 'Content-Type: application/json')
	[ -n "$ORIGIN" ] && args+=(-H "Origin: $ORIGIN")
	[ -n "$body" ] && args+=(-d "$body")

	local response status
	response="$(curl "${args[@]}" "$API_BASE$path")"
	status="${response##*$'\n'}"
	response="${response%$'\n'*}"

	case "$status" in
		2*) printf '%s' "$response" | jq '.data' ;;
		*)  fail "$method $path -> HTTP $status: $response" ;;
	esac
}

step "Configured engines (GET /aeo/providers)"
providers="$(api GET /aeo/providers)"
echo "$providers" | jq -r '.[] | "  \(.name)\t\(.model)\tconfigured=\(.configured)"'
configured_count="$(echo "$providers" | jq '[.[] | select(.configured)] | length')"
[ "$configured_count" -gt 0 ] || fail "no engine has credentials; nothing live to smoke"
echo "  -> $configured_count engine(s) with credentials"

step "Brand profile (GET /aeo/profile)"
# 404 here is a legitimate state, so this one call bypasses the status check.
profile_args=(-sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN")
[ -n "$ORIGIN" ] && profile_args+=(-H "Origin: $ORIGIN")
profile_status="$(curl "${profile_args[@]}" "$API_BASE/aeo/profile")"
if [ "$profile_status" = "404" ]; then
	fail "no brand profile configured — save one at /aeo/settings first, or the run has nothing to detect"
elif [ "$profile_status" != "200" ]; then
	fail "GET /aeo/profile -> HTTP $profile_status"
fi
api GET /aeo/profile | jq -r '"  brand: \(.brand_name)\n  aliases: \(.brand_aliases // [] | join(", "))\n  owned: \(.owned_domains // [] | join(", "))\n  competitors: \([.competitors[]?.name] | join(", "))"'

if [ "$DO_RUN" -eq 0 ]; then
	step "Read-only mode (-n): skipping prompt creation and the run"
	step "Dashboard (GET /aeo/dashboard?days=30)"
	api GET '/aeo/dashboard?days=30' | jq '{total_answers, failed_answers, brand_mentions, visibility, providers: [.by_provider[]?.provider]}'
	step "Citations (GET /aeo/citations?days=30)"
	api GET '/aeo/citations?days=30' | jq '{total_citations, answers_with_citations, owned_citation_rate}'
	echo
	echo "Done (read-only)."
	exit 0
fi

step "Creating the smoke prompt (POST /aeo/prompts)"
prompt_payload="$(jq -nc --arg text "$PROMPT_TEXT" '{prompts: [$text]}')"
created="$(api POST /aeo/prompts "$prompt_payload")"
prompt_id="$(echo "$created" | jq -r '.[0].id')"
[ -n "$prompt_id" ] && [ "$prompt_id" != "null" ] || fail "could not read the created prompt id"
echo "  prompt #$prompt_id: $PROMPT_TEXT"

cleanup() {
	if [ "$KEEP_PROMPT" -eq 0 ] && [ -n "${prompt_id:-}" ]; then
		printf '\n=== Removing the smoke prompt #%s (DELETE /aeo/prompts/%s)\n' "$prompt_id" "$prompt_id"
		# Soft delete: recorded answers survive, so the run stays visible in the
		# dashboard history. Needs an admin token; a sales token gets 403 here.
		curl -sS -o /dev/null -X DELETE \
			-H "Authorization: Bearer $TOKEN" \
			${ORIGIN:+-H "Origin: $ORIGIN"} \
			"$API_BASE/aeo/prompts/$prompt_id" || true
	fi
}
trap cleanup EXIT

active_prompts="$(api GET '/aeo/prompts?active_only=true&limit=100' | jq 'length')"
projected=$((active_prompts * configured_count))
step "About to spend provider credit"
echo "  active prompts: $active_prompts"
echo "  engines:        $configured_count"
echo "  projected calls: $projected"
if [ "$ASSUME_YES" -eq 0 ]; then
	printf '  continue? [y/N] '
	read -r answer
	case "$answer" in y|Y|yes|YES) ;; *) echo "  aborted"; exit 0 ;; esac
fi

step "Starting the run (POST /aeo/runs)"
run="$(api POST /aeo/runs '')"
run_id="$(echo "$run" | jq -r '.id')"
echo "  run #$run_id status=$(echo "$run" | jq -r '.status') total_queries=$(echo "$run" | jq -r '.total_queries')"

step "Polling until the run settles (GET /aeo/runs/$run_id)"
status="running"
for _ in $(seq 1 "$POLL_ATTEMPTS"); do
	sleep "$POLL_INTERVAL"
	run="$(api GET "/aeo/runs/$run_id")"
	status="$(echo "$run" | jq -r '.status')"
	printf '  status=%s failed=%s\n' "$status" "$(echo "$run" | jq -r '.failed_queries')"
	[ "$status" = "running" ] || break
done
if [ "$status" = "running" ]; then
	echo "  still running after $((POLL_ATTEMPTS * POLL_INTERVAL))s — leaving it alone."
	echo "  (a run stranded by a crash is swept automatically: at startup, and by"
	echo "   the next StartRun once it is older than 6h. No manual UPDATE needed.)"
	exit 1
fi
echo "  final: $(echo "$run" | jq -c '{status, total_queries, failed_queries, started_at, completed_at}')"

step "Recorded answers (GET /aeo/prompts/$prompt_id/answers?run_id=$run_id)"
answers="$(api GET "/aeo/prompts/$prompt_id/answers?run_id=$run_id")"
echo "$answers" | jq -r '.[] | "  \(.provider)/\(.model)\tmentioned=\(.brand_mentioned)\tpos=\(.first_mention_pos)\tcitations=\(.citations | length)\tlatency=\(.latency_ms)ms\terror=\(.error // "-")"'
echo
echo "  first 300 characters of each answer:"
echo "$answers" | jq -r '.[] | "  --- \(.provider) ---\n  \(.answer_text[0:300] | gsub("\n"; " "))"'

step "Citations recorded from this run"
echo "$answers" | jq -r '[.[].citations[]?] | if length == 0 then "  (none — expected unless a search-grounded engine such as perplexity is configured)" else (.[] | "  \(.domain)\towned=\(.is_owned)\tcompetitor=\(.competitor_name // "-")\t\(.url)") end'

step "Dashboard (GET /aeo/dashboard?days=7)"
api GET '/aeo/dashboard?days=7' | jq '{total_answers, failed_answers, brand_mentions, visibility, by_provider, share_of_voice}'

step "Citation report (GET /aeo/citations?days=7)"
api GET '/aeo/citations?days=7' | jq '{total_answers, total_citations, answers_with_citations, owned_citation_rate, by_company}'

echo
echo "Smoke complete. Nothing above is asserted — read it."
