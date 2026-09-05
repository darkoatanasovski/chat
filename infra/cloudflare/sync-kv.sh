#!/usr/bin/env bash
# Sync the placement map from the config DB into the Worker's KV namespace.
# The config DB is the source of truth; this pushes apikey/appid -> {region,
# shard} to Cloudflare KV so the edge Worker (src/worker.js) can route without
# touching Postgres. Run it on every placement/credential change (and on a
# schedule as a safety net) — the edge equivalent of internal/appconfig's
# invalidate-on-change / TTL refresh.
#
# For each active credential it writes two entries so both token shapes route:
#   apikey:<key>      -> {"region":..,"shard":..}   (app tokens, ?api_key=)
#   appid:<app_id>    -> {"region":..,"shard":..}   (end-user tokens)
#
# Requires: psql, python3, curl.
# Env:
#   CONFIG_DATABASE_URL   config DB DSN
#   CF_ACCOUNT_ID         Cloudflare account id
#   CF_KV_NAMESPACE_ID    the PLACEMENT namespace id (from `wrangler kv namespace create`)
#   CF_API_TOKEN          a Cloudflare API token with Workers KV Storage: Edit
set -euo pipefail

: "${CONFIG_DATABASE_URL:?CONFIG_DATABASE_URL is required}"
: "${CF_ACCOUNT_ID:?CF_ACCOUNT_ID is required}"
: "${CF_KV_NAMESPACE_ID:?CF_KV_NAMESPACE_ID is required}"
: "${CF_API_TOKEN:?CF_API_TOKEN is required}"

echo "==> reading placements from config DB"
rows=$(psql "$CONFIG_DATABASE_URL" -tAF $'\t' -c "
  SELECT c.key, a.app_id, a.region, a.shard
  FROM app_credentials c
  JOIN apps a ON a.app_id = c.app_id
  WHERE c.revoked_at IS NULL AND a.region IS NOT NULL AND a.shard IS NOT NULL
")

# Build the Cloudflare KV bulk payload (array of {key, value}) from the rows.
payload=$(printf '%s\n' "$rows" | python3 -c '
import sys, json
seen_app = set()
items = []
for line in sys.stdin:
    line = line.rstrip("\n")
    if not line.strip():
        continue
    key, app_id, region, shard = line.split("\t")
    value = json.dumps({"region": region, "shard": shard})
    items.append({"key": f"apikey:{key}", "value": value})
    if app_id not in seen_app:
        seen_app.add(app_id)
        items.append({"key": f"appid:{app_id}", "value": value})
print(json.dumps(items))
')

count=$(printf '%s' "$payload" | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))')
if [ "$count" = "0" ]; then
  echo "==> nothing to sync (no active placed credentials)"
  exit 0
fi

# The bulk endpoint accepts up to 10,000 keys per request; chunk beyond that
# if the platform ever grows past ~5,000 credentials.
echo "==> writing $count KV entries"
curl -fsS -X PUT \
  "https://api.cloudflare.com/client/v4/accounts/${CF_ACCOUNT_ID}/storage/kv/namespaces/${CF_KV_NAMESPACE_ID}/bulk" \
  -H "Authorization: Bearer ${CF_API_TOKEN}" \
  -H "Content-Type: application/json" \
  --data "$payload" >/dev/null

echo "==> sync complete"
