# Demo App

`demo/` is a minimal Next.js (App Router, TypeScript) app whose only purpose
is exercising the platform's features by hand — it is a test harness, not a
polished product. There's no styling framework, no state management library,
no component abstraction beyond three pages: it talks to the real REST API
and real WebSocket gateway with plain `fetch` and `WebSocket`.

## Running it

Stack must already be up and migrated (`platform-up` skill or
[../operations/local-development.md](../operations/local-development.md)):

```bash
cd demo
npm install
npm run dev     # http://localhost:3000
```

## Pages

- **`/`** — create a test user (display name + region) or continue as the
  last one created (stored in `localStorage`, key `chat-demo-profile`). No
  password — see [../platform/security.md](../platform/security.md).
- **`/channels`** — list your channels (`GET /users/me/channels`), create a
  new one, add a member by `user_id`. Your own `user_id` is shown here so
  you can copy it into a second browser session to test membership.
- **`/channels/[id]`** — the main test surface:
  - Live message list, connected via WebSocket, with a visible `ws: open /
    closed / connecting` badge.
  - **Load older** — cursor pagination (`before=`), never `OFFSET`.
  - **Send** — generates a fresh `client_message_id` per click.
  - **Retry last (idempotency demo)** — re-sends the *same*
    `client_message_id` as the last send and logs that the response comes
    back with the identical `message_id`/`sequence` — proof no duplicate was
    created.
  - **Spam send 25 (rate-limit demo)** — fires 25 sends back-to-back and
    reports how many were `201` vs `429`, demonstrating
    `messages_per_minute` under load.
  - **Disconnect** / **Reconnect + recover** — manually closes the
    WebSocket, then reopens it and re-fetches recent history to fill any gap
    (see
    [../platform/realtime-delivery.md](../platform/realtime-delivery.md#reconnection-and-gap-recovery-20)).
  - **Event log** — a running log of every notable client-side event
    (connect/disconnect, delivery, retry result, rate-limit outcome).

## Testing cross-region behavior

Open two browser profiles (or one normal + one incognito), create a user in
create a channel as one, and add the other as a member using their `user_id`
from the `/channels` page. All of an App's users and channels live in the same
cell (the App's placement), so messages are delivered within that cell's
realtime fanout — see [../platform/multi-region.md](../platform/multi-region.md)
and [../adr/0006-cell-based-tenant-routing.md](../adr/0006-cell-based-tenant-routing.md).

## What this app is not

Not a reference client implementation, not styled for end users, not
covering every edge case a real chat client would need (no read receipts,
no attachments, no offline queue beyond the one gap-recovery flow above —
matching V1's feature scope exactly, INSTRUCTIONS.md §1).
