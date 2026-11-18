#!/usr/bin/env bash
# Manual smoke check of quote PDF output against a running GopherCRM backend:
# the real chain, headless Chrome -> the web app's /quote-render page ->
# POST /quotes/public/render -> the printed document. Nothing is faked.
#
# It logs in as an admin, creates a customer, a product and one quote with one
# line whose description is unique, downloads GET /quotes/<id>/pdf and fails
# unless the answer is 200 application/pdf starting with %PDF. The file is
# written to the path given as the first argument. On success it prints
# exactly two lines:
#
#   number=<quote number>
#   line=<line description>
#
# so the caller can look both up in the text of the file, for example with
# pdftotext. With SMOKE_EMAIL=1 it also marks the quote sent and emails it,
# and prints a third line, emailed=yes: with SMTP_HOST empty the server's log
# then names the attachment.
#
#   BASE_URL=http://localhost:18095/api/v1 \
#   ADMIN_EMAIL=admin@example.com ADMIN_PASSWORD='...' \
#   scripts/e2e/quote-pdf-smoke.sh /tmp/quote.pdf
#
# The backend must run with QUOTE_PDF_CHROME_PATH set and an APP_BASE_URL it
# can reach; the recipe is in docs/DEVELOPMENT.md. The script starts nothing,
# prints neither the password nor a token, and creates data, so it refuses a
# BASE_URL whose host is not localhost or 127.0.0.1. It is a manual tool: no
# workflow, Makefile target or gate runs it.
set -u

BASE_URL="${BASE_URL:-http://localhost:8090/api/v1}"
ADMIN_EMAIL="${ADMIN_EMAIL:-}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
OUTPUT="${1:-}"

fail() { echo "quote-pdf-smoke: $*" >&2; exit 1; }

[ -n "$OUTPUT" ] || fail "usage: BASE_URL=... ADMIN_EMAIL=... ADMIN_PASSWORD=... $0 <output.pdf>"
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASSWORD" ] || fail "ADMIN_EMAIL and ADMIN_PASSWORD are required"
BASE_URL="${BASE_URL%/}"

HOST=$(python3 -c 'import sys,urllib.parse;print(urllib.parse.urlsplit(sys.argv[1]).hostname or "")' "$BASE_URL" 2>/dev/null)
case "$HOST" in
  localhost|127.0.0.1) ;;
  *) fail "BASE_URL must point at localhost or 127.0.0.1 (this script creates data); refusing the given host" ;;
esac

WORK=$(mktemp -d) || fail "cannot create a temporary directory"
trap 'rm -rf "$WORK"' EXIT

# field <python expression over d> reads one value from a JSON document on stdin.
field() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
# body <key=value>... builds a JSON object; values are JSON when they parse, strings otherwise.
body() {
  python3 -c '
import sys, json
out = {}
for pair in sys.argv[1:]:
    key, _, value = pair.partition("=")
    try:
        out[key] = json.loads(value)
    except ValueError:
        out[key] = value
print(json.dumps(out))' "$@"
}

LOGIN=$(body "email=\"$ADMIN_EMAIL\"" "password=$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$ADMIN_PASSWORD")")
TOKEN=$(curl -sS -m 30 -X POST "$BASE_URL/auth/login" -H 'Content-Type: application/json' -d "$LOGIN" | field 'd["data"]["token"]')
[ -n "$TOKEN" ] || fail "admin login failed"

# api <method> <path> [json] -> the response body; fails on a non-2xx answer.
api() {
  local method="$1" path="$2" data="${3:-}" status
  if [ -n "$data" ]; then
    status=$(curl -sS -m 60 -o "$WORK/response.json" -w '%{http_code}' -X "$method" "$BASE_URL/$path" \
      -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$data")
  else
    status=$(curl -sS -m 60 -o "$WORK/response.json" -w '%{http_code}' -X "$method" "$BASE_URL/$path" \
      -H "Authorization: Bearer $TOKEN")
  fi
  case "$status" in
    2??) cat "$WORK/response.json" ;;
    *) fail "$method /$path answered $status: $(field 'd["error"]["code"] + " " + d["error"]["message"]' < "$WORK/response.json")" ;;
  esac
}

SUFFIX=$(python3 -c 'import secrets;print(secrets.token_hex(4))')
LINE="PDF smoke line $SUFFIX"

CUSTOMER_ID=$(api POST customers "$(body first_name=Smoke last_name=Customer "email=pdf-smoke-$SUFFIX@example.com")" | field 'd["data"]["id"]') || exit 1
[ -n "$CUSTOMER_ID" ] || fail "the customer was not created"
PRODUCT_ID=$(api POST products "$(body "name=PDF smoke product $SUFFIX" unit_price_cents=12345 currency=EUR)" | field 'd["data"]["id"]') || exit 1
[ -n "$PRODUCT_ID" ] || fail "the product was not created"
QUOTE_ID=$(api POST quotes "$(body "title=PDF smoke quote $SUFFIX" "customer_id=$CUSTOMER_ID" currency=EUR)" | field 'd["data"]["id"]') || exit 1
[ -n "$QUOTE_ID" ] || fail "the quote was not created"
NUMBER=$(api POST "quotes/$QUOTE_ID/lines" "$(body "product_id=$PRODUCT_ID" quantity=2 discount_bp=0 "description=$LINE")" | field 'd["data"]["number"]') || exit 1
[ -n "$NUMBER" ] || fail "the line was not added"

# The download: the status, the content type and the first bytes all count.
STATUS=$(curl -sS -m 60 -o "$WORK/quote.pdf" -D "$WORK/headers.txt" -w '%{http_code}' \
  "$BASE_URL/quotes/$QUOTE_ID/pdf" -H "Authorization: Bearer $TOKEN")
if [ "$STATUS" != "200" ]; then
  fail "GET /quotes/$QUOTE_ID/pdf answered $STATUS: $(field 'd["error"]["code"] + " " + d["error"]["message"]' < "$WORK/quote.pdf")"
fi
grep -qi '^content-type: *application/pdf' "$WORK/headers.txt" || fail "GET /quotes/$QUOTE_ID/pdf is not application/pdf"
[ "$(head -c 4 "$WORK/quote.pdf")" = "%PDF" ] || fail "GET /quotes/$QUOTE_ID/pdf does not start with %PDF"
cp "$WORK/quote.pdf" "$OUTPUT" || fail "cannot write $OUTPUT"

echo "number=$NUMBER"
echo "line=$LINE"

if [ "${SMOKE_EMAIL:-}" = "1" ]; then
  api POST "quotes/$QUOTE_ID/status" "$(body status=sent)" > /dev/null || exit 1
  EMAILED=$(api POST "quotes/$QUOTE_ID/email" | field 'd["data"]["last_emailed_at"]') || exit 1
  [ -n "$EMAILED" ] && [ "$EMAILED" != "None" ] || fail "the quote was not marked as emailed"
  echo "emailed=yes"
fi
