#!/usr/bin/env bash
# Seeds four demo organizations (one per tier), one App per org with its
# first API credential, and a couple of demo end-users/a channel so there's
# something to look at immediately (e.g. in demo/) without manually curling.
# Safe to re-run — it just creates new orgs/apps/users each time.
set -euo pipefail

# Everything goes through the router (the global endpoint): it forwards
# control-plane paths (/organizations, /apps, /dashboard) to the control
# service and data-plane paths (/users, /channels) to the app's cell by
# apikey. So seed exercises the real routing path end to end.
API="${API:-http://localhost:8080}"

echo "==> creating demo organizations (one per tier) and their apps"

declare -A ORG_ID APP_ID APP_KEY APP_SECRET
for TIER in FREE PRO BUSINESS ENTERPRISE; do
  ORG=$(curl -sf -X POST "$API/organizations" -d "{\"name\":\"Demo Org ($TIER)\",\"tier\":\"$TIER\"}")
  ORG_TOKEN=$(echo "$ORG" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
  ORG_ID[$TIER]=$(echo "$ORG" | python3 -c "import sys,json;print(json.load(sys.stdin)['org_id'])")

  APP=$(curl -sf -X POST "$API/organizations/${ORG_ID[$TIER]}/apps" \
    -H "Authorization: Bearer $ORG_TOKEN" -d '{"name":"Demo App"}')
  APP_ID[$TIER]=$(echo "$APP" | python3 -c "import sys,json;print(json.load(sys.stdin)['app_id'])")
  APP_KEY[$TIER]=$(echo "$APP" | python3 -c "import sys,json;print(json.load(sys.stdin)['credential']['key'])")
  APP_SECRET[$TIER]=$(echo "$APP" | python3 -c "import sys,json;print(json.load(sys.stdin)['credential']['secret'])")
done

echo "==> creating demo users (alice, bob) under the FREE-tier demo app"
# POST /users now runs on a short-lived app JWT (requireAppJWT), not the raw
# key:secret directly — exchange once per region via POST /apps/token
# (requireAppCredentials, Basic auth) first, same as demo/lib/api.ts does.
APP_TOKEN=$(curl -sf -X POST "$API/apps/token" -u "${APP_KEY[FREE]}:${APP_SECRET[FREE]}" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
ALICE=$(curl -sf -X POST "$API/users" -H "Authorization: Bearer $APP_TOKEN" -d '{"display_name":"Alice"}')
BOB=$(curl -sf -X POST "$API/users" -H "Authorization: Bearer $APP_TOKEN" -d '{"display_name":"Bob"}')

ALICE_TOKEN=$(echo "$ALICE" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
ALICE_ID=$(echo "$ALICE" | python3 -c "import sys,json;print(json.load(sys.stdin)['user_id'])")
BOB_TOKEN=$(echo "$BOB" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
BOB_ID=$(echo "$BOB" | python3 -c "import sys,json;print(json.load(sys.stdin)['user_id'])")

echo "==> creating demo channel and adding bob"
CHANNEL=$(curl -sf -X POST "$API/channels" -H "Authorization: Bearer $ALICE_TOKEN" -d '{"name":"general"}')
CHANNEL_ID=$(echo "$CHANNEL" | python3 -c "import sys,json;print(json.load(sys.stdin)['channel_id'])")
curl -sf -X POST "$API/channels/$CHANNEL_ID/members" -H "Authorization: Bearer $ALICE_TOKEN" -d "{\"user_id\":\"$BOB_ID\"}" >/dev/null

echo "==> sending a welcome message from bob"
CMID=$(python3 -c "import uuid;print(uuid.uuid4())")
curl -sf -X POST "$API/channels/$CHANNEL_ID/messages" -H "Authorization: Bearer $BOB_TOKEN" \
  -d "{\"client_message_id\":\"$CMID\",\"body\":\"hello from bob (seeded)\"}" >/dev/null

cat <<EOF

==> seed complete

demo app credentials (one per tier — org_id / app_id / key:secret):
  FREE:       org_id=${ORG_ID[FREE]}       app_id=${APP_ID[FREE]}       ${APP_KEY[FREE]}:${APP_SECRET[FREE]}
  PRO:        org_id=${ORG_ID[PRO]}        app_id=${APP_ID[PRO]}        ${APP_KEY[PRO]}:${APP_SECRET[PRO]}
  BUSINESS:   org_id=${ORG_ID[BUSINESS]}   app_id=${APP_ID[BUSINESS]}   ${APP_KEY[BUSINESS]}:${APP_SECRET[BUSINESS]}
  ENTERPRISE: org_id=${ORG_ID[ENTERPRISE]} app_id=${APP_ID[ENTERPRISE]} ${APP_KEY[ENTERPRISE]}:${APP_SECRET[ENTERPRISE]}

Put these into demo/.env.local as:
  NEXT_PUBLIC_DEMO_APP_CREDENTIALS_FREE=${APP_KEY[FREE]}:${APP_SECRET[FREE]}
  NEXT_PUBLIC_DEMO_APP_CREDENTIALS_PRO=${APP_KEY[PRO]}:${APP_SECRET[PRO]}
  NEXT_PUBLIC_DEMO_APP_CREDENTIALS_BUSINESS=${APP_KEY[BUSINESS]}:${APP_SECRET[BUSINESS]}
  NEXT_PUBLIC_DEMO_APP_CREDENTIALS_ENTERPRISE=${APP_KEY[ENTERPRISE]}:${APP_SECRET[ENTERPRISE]}

alice: user_id=$ALICE_ID token=$ALICE_TOKEN (FREE-tier demo app)
bob:   user_id=$BOB_ID token=$BOB_TOKEN (FREE-tier demo app)
channel: $CHANNEL_ID

Paste alice's or bob's token into demo/ localStorage key "chat-demo-profile", or just
sign in fresh at http://localhost:3000 and use "Add member" with the user_id above.
EOF
