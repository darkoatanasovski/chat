# Multi-Region

Three logical regions — `eu`, `us`, `asia` — per INSTRUCTIONS.md §4. This
deployment simulates them on one machine: three `api` instances and three
`gateway` instances, one pair per region, sharing one Kafka broker and one
Valkey instance (see `deploy/docker-compose.yml`). A real deployment would
put each region's `api`/`gateway` pair in its own physical datacenter behind
geo-routing; the application code doesn't know or care which is true.

## The one invariant: one authoritative writer per channel

Every channel has exactly one `home_region` (INSTRUCTIONS.md §5), set once
at creation to whichever region's `api` instance handled `POST /channels`.
All writes to that channel — sending a message, adding a member — are
authoritative only when performed by that region's `api` instance. This
prevents the distributed-write-consistency problems of letting any region
write any channel: sequence assignment (`internal/messages`) is a
single-Postgres-transaction operation, which only works if exactly one
process can ever be assigning the next sequence number for a given channel
at a time.

## Cross-region forwarding

If `api-us` receives a write for a channel whose `home_region` is `eu`, it
doesn't touch Postgres at all — it proxies the original request verbatim to
`api-eu` and relays the response back:

```go
// cmd/api/forward.go
func (a *App) forwardToHomeRegion(w http.ResponseWriter, r *http.Request, homeRegion string, body []byte) {
    peerURL := a.cfg.PeerAPIURL[homeRegion]
    req, _ := http.NewRequestWithContext(r.Context(), r.Method, peerURL+r.URL.Path, bytes.NewReader(body))
    req.Header.Set("Authorization", r.Header.Get("Authorization"))
    resp, _ := a.peerClient.Do(req)
    // relay resp back to the original caller
}
```

`api-eu` runs the *exact same handler* — it just happens to check
`homeRegion == a.cfg.Region` and finds a match, so it writes locally instead
of forwarding again. There's no separate "internal" API; forwarding reuses
the public REST surface over the docker-compose internal network
(`PEER_API_EU_URL`/`_US_URL`/`_ASIA_URL` env vars).

Only writes forward. Reads (`GET /channels/{id}/messages`) are served by
whichever instance received the request, directly against the resolved
physical shard — in this single-machine deployment every `api` instance can
reach every shard Postgres instance, so there's no correctness reason to
forward a read. A real geo-distributed deployment would add async regional
read replicas (§26) so reads stay low-latency without needing every region
to reach every shard over a WAN; that's additive infrastructure, not an
architecture change — the read path here would just start reading from a
local replica instead of the primary.

`docs/adr/0002-channel-home-region.md` has the fuller reasoning and
trade-offs.

## What is and isn't checked before forwarding

The receiving instance does authentication, membership, and rate-limit
checks *before* forwarding (fail fast, one fewer network hop on a request
that's going to be rejected anyway). The home-region instance does not
re-check membership on a forwarded request — it trusts the request came from
another instance sharing the same `AUTH_SECRET` trust domain. This is a
reasonable simplification for a single-operator deployment; a
multi-tenant/zero-trust deployment would want mutual TLS or signed
service-to-service tokens on the internal hop instead of relying on shared
`Authorization` header pass-through.

## Realtime is fully cross-region

A channel's members can be in any region regardless of the channel's home
region. Every `gateway` instance consumes the *entire* `message.created`
Kafka topic — not filtered by region — and checks its own locally-connected
users against the message's channel membership. See
[realtime-delivery.md](realtime-delivery.md).

## Regional replication (not built, by design)

INSTRUCTIONS.md §26 describes async Kafka-driven replication of a channel's
state into non-home regions for local reads. V1 doesn't need this — there's
one shared Postgres reachable from every `api` instance — but nothing in the
data model assumes it never exists: `outbox_events` already carries every
message-created event through Kafka, which is exactly the mechanism a future
regional-replica consumer would subscribe to.
