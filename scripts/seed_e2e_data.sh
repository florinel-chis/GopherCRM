#!/usr/bin/env bash
# Seed a running GopherCRM backend with a small, realistic data set for E2E runs.
#
# The admin account must already exist (cmd/create-admin); everything else is
# created through the public API so the script works unchanged on MySQL and
# SQLite backends.
#
#   BASE_URL=http://localhost:8090/api/v1 \
#   ADMIN_EMAIL=test-admin@gocrm.test ADMIN_PASSWORD='AdminPass123!' \
#   scripts/seed_e2e_data.sh
#
# Re-running is safe: duplicate emails and label names are reported and skipped.
set -u

BASE_URL="${BASE_URL:-http://localhost:8090/api/v1}"
ADMIN_EMAIL="${ADMIN_EMAIL:-test-admin@gocrm.test}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-AdminPass123!}"

json() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

TOKEN=$(curl -sS -X POST "$BASE_URL/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" | json 'd["data"]["token"]')
if [ -z "$TOKEN" ]; then echo "seed: admin login failed" >&2; exit 1; fi
ADMIN_ID=$(curl -sS "$BASE_URL/users" -H "Authorization: Bearer $TOKEN" \
  | json '[u["id"] for u in d["data"] if u["email"]=="'"$ADMIN_EMAIL"'"][0]')

post() { # post <path> <json> -> id (empty on non-2xx)
  curl -sS -X POST "$BASE_URL/$1" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d "$2" | json 'd["data"]["id"]'
}

created=0; skipped=0
note() { if [ -n "$1" ]; then created=$((created+1)); else skipped=$((skipped+1)); fi; }

# Users (elevated roles only exist via the admin API)
SALES_ID=$(post users '{"email":"seed-sales@gocrm.test","password":"SeedSales123!","first_name":"Sonia","last_name":"Sales","role":"sales"}'); note "$SALES_ID"
SUPPORT_ID=$(post users '{"email":"seed-support@gocrm.test","password":"SeedSupport1!","first_name":"Radu","last_name":"Support","role":"support"}'); note "$SUPPORT_ID"
SALES_ID=${SALES_ID:-$ADMIN_ID}; SUPPORT_ID=${SUPPORT_ID:-$ADMIN_ID}

# Customers
CUST_IDS=()
for c in \
  '{"first_name":"Elena","last_name":"Marin","email":"seed-elena@corvid.example","phone":"+40721000001","company":"Corvid SRL","position":"CTO"}' \
  '{"first_name":"Mihai","last_name":"Pop","email":"seed-mihai@albina.example","phone":"+40721000002","company":"Albina SA"}' \
  '{"first_name":"Ana","last_name":"Ionescu","email":"seed-ana@brad.example","company":"Brad Digital"}' \
  '{"first_name":"Dan","last_name":"Vasile","email":"seed-dan@cires.example","phone":"+40721000004"}' \
  '{"first_name":"Ioana","last_name":"Toma","email":"seed-ioana@dor.example","company":"Dor Media","position":"Founder"}' ; do
  ID=$(post customers "$c"); note "$ID"; [ -n "$ID" ] && CUST_IDS+=("$ID")
done

# Leads — admin-created leads must name an owner
for l in \
  "{\"first_name\":\"Luca\",\"last_name\":\"Nou\",\"email\":\"seed-luca@lead.example\",\"source\":\"website\",\"owner_id\":$SALES_ID}" \
  "{\"first_name\":\"Maria\",\"last_name\":\"Contactata\",\"email\":\"seed-maria@lead.example\",\"phone\":\"+40722000002\",\"source\":\"referral\",\"status\":\"contacted\",\"owner_id\":$SALES_ID}" \
  "{\"first_name\":\"Paul\",\"last_name\":\"Telefon\",\"phone\":\"+40722000003\",\"source\":\"cold_call\",\"owner_id\":$SALES_ID}" \
  "{\"first_name\":\"Dana\",\"last_name\":\"Calificata\",\"email\":\"seed-dana@lead.example\",\"status\":\"qualified\",\"source\":\"other\",\"owner_id\":$ADMIN_ID}" \
  "{\"first_name\":\"Vlad\",\"last_name\":\"Pierdut\",\"email\":\"seed-vlad@lead.example\",\"status\":\"unqualified\",\"source\":\"website\",\"owner_id\":$ADMIN_ID}" ; do
  ID=$(post leads "$l"); note "$ID"
done

# Labels
LABEL_IDS=()
for lb in '{"name":"seed-urgent","color":"#d32f2f"}' '{"name":"seed-follow-up","color":"#1976d2"}' '{"name":"seed-billing","color":"#388e3c"}'; do
  ID=$(post labels "$lb"); note "$ID"; [ -n "$ID" ] && LABEL_IDS+=("$ID")
done

# Tasks
DUE=$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=7)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
for t in \
  "{\"title\":\"Seed: call Corvid about renewal\",\"priority\":\"high\",\"assigned_to_id\":$SALES_ID,\"due_date\":\"$DUE\"${LABEL_IDS:+,\"label_ids\":[${LABEL_IDS[0]}]}}" \
  "{\"title\":\"Seed: prepare Q4 demo\",\"description\":\"Environment plus data set\",\"priority\":\"medium\",\"assigned_to_id\":$SALES_ID}" \
  "{\"title\":\"Seed: verify backup restore\",\"priority\":\"low\",\"assigned_to_id\":$SUPPORT_ID}" \
  "{\"title\":\"Seed: onboard Dor Media\",\"assigned_to_id\":$ADMIN_ID,\"due_date\":\"$DUE\"}" ; do
  ID=$(post tasks "$t"); note "$ID"
done

# Tickets need a customer
if [ "${#CUST_IDS[@]}" -gt 0 ]; then
  C0=${CUST_IDS[0]}; C1=${CUST_IDS[1]:-$C0}
  for tk in \
    "{\"title\":\"Seed: cannot export leads\",\"description\":\"Export button returns an error\",\"priority\":\"high\",\"customer_id\":$C0,\"assigned_to_id\":$SUPPORT_ID}" \
    "{\"title\":\"Seed: invoice address wrong\",\"description\":\"Billing address is outdated\",\"priority\":\"medium\",\"customer_id\":$C1}" \
    "{\"title\":\"Seed: feature question\",\"description\":\"Asking about the forms module\",\"priority\":\"low\",\"customer_id\":$C0}" ; do
    ID=$(post tickets "$tk"); note "$ID"
  done
fi

echo "seed: $created created, $skipped skipped (already present or rejected)"
