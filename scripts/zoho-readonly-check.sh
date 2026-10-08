#!/usr/bin/env bash
# Runs every READ-ONLY Zoho Books call that Purchase Desk uses, with the credentials in .env,
# and prints whether each one works. It creates or changes nothing in Zoho.
# Usage: ./scripts/zoho-readonly-check.sh            (from the project folder)
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; . ./.env; set +a

BASE="$ZOHO_API_URL/books/v3"
ORG="organization_id=$ZOHO_ORG_ID"
ok=0; bad=0

echo "1. Access token from refresh token ($ZOHO_ACCOUNTS_URL/oauth/v2/token)"
TOKEN_JSON=$(curl -s -X POST "$ZOHO_ACCOUNTS_URL/oauth/v2/token" \
  -d "refresh_token=$ZOHO_REFRESH_TOKEN&client_id=$ZOHO_CLIENT_ID&client_secret=$ZOHO_CLIENT_SECRET&grant_type=refresh_token")
AT=$(printf '%s' "$TOKEN_JSON" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("access_token",""))')
if [ -z "$AT" ]; then echo "   FAILED: $TOKEN_JSON"; exit 1; fi
echo "   OK (token valid for one hour)"

check() { # name, path-with-query, json key to count
  local name="$1" path="$2" key="$3"
  local out
  out=$(curl -s "$BASE$path" -H "Authorization: Zoho-oauthtoken $AT")
  local res
  res=$(printf '%s' "$out" | python3 -c '
import sys, json
key = sys.argv[1]
try:
    d = json.load(sys.stdin)
except Exception as e:
    print("FAILED: not JSON"); sys.exit()
if d.get("code") != 0:
    print("FAILED: code %s, %s" % (d.get("code"), d.get("message"))); sys.exit()
v = d.get(key)
n = len(v) if isinstance(v, (list, dict)) else ("present" if v is not None else "missing")
print("OK, %s: %s" % (key, n))' "$key")
  echo "$name"; echo "   $res"
  case "$res" in OK*) ok=$((ok+1));; *) bad=$((bad+1));; esac
}

check "2. Organisations (GET /organizations)"                        "/organizations?$ORG"                                         organizations
check "3. Chart of accounts, page 1 (GET /chartofaccounts)"           "/chartofaccounts?$ORG&filter_by=AccountType.All&per_page=200&page=1" chartofaccounts
check "4. Taxes and tax groups (GET /settings/taxes)"                 "/settings/taxes?$ORG"                                        taxes
check "5. Reporting tags (GET /reportingtags)"                        "/reportingtags?$ORG"                                         tags
check "6. Locations (GET /locations, optional)"                       "/locations?$ORG"                                             locations
check "7. Vendor contacts, page 1 (GET /contacts?contact_type=vendor)" "/contacts?$ORG&contact_type=vendor&filter_by=Status.All&per_page=200&page=1" contacts
check "8. Bill custom fields (GET /settings/customfields?entity=bill)" "/settings/customfields?$ORG&entity=bill"                      customfields
SINCE=$(python3 -c 'import datetime;print((datetime.datetime.now()-datetime.timedelta(days=7)).strftime("%Y-%m-%dT%H:%M:%S+0530"))')
check "9. Bills changed in the last 7 days (GET /bills?last_modified_time=)" "/bills?$ORG&last_modified_time=$(python3 -c "import urllib.parse;print(urllib.parse.quote('$SINCE'))")&per_page=5" bills

FIRST_BILL=$(curl -s "$BASE/bills?$ORG&per_page=1" -H "Authorization: Zoho-oauthtoken $AT" | python3 -c 'import sys,json; b=json.load(sys.stdin).get("bills",[]); print(b[0]["bill_id"] if b else "")')
if [ -n "$FIRST_BILL" ]; then
  check "10. One bill (GET /bills/{bill_id})"            "/bills/$FIRST_BILL?$ORG"          bill
  check "11. Payments on a bill (GET /bills/{id}/payments)" "/bills/$FIRST_BILL/payments?$ORG" payments
fi
TAG=$(curl -s "$BASE/reportingtags?$ORG" -H "Authorization: Zoho-oauthtoken $AT" | python3 -c 'import sys,json; t=json.load(sys.stdin).get("tags",[]); print(t[0]["tag_id"] if t else "")')
if [ -n "$TAG" ]; then
  check "12. Options of the first reporting tag (GET /reportingtags/{id})" "/reportingtags/$TAG?$ORG" reporting_tag
else
  echo "12. Reporting tag options: skipped, this organisation has no reporting tag yet (create one named Property)"
fi

KEY=$(curl -s "$BASE/settings/customfields?$ORG&entity=bill" -H "Authorization: Zoho-oauthtoken $AT" | python3 -c 'import sys,json; f=json.load(sys.stdin).get("customfields",{}).get("bill",[]); print("yes" if any(x.get("api_name")=="cf_purchase_desk_id" for x in f) else "no")')
echo
echo "Bill custom field cf_purchase_desk_id exists: $KEY"
echo "Read calls OK: $ok, failed: $bad"
