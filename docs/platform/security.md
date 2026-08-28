# Security

Implements INSTRUCTIONS.md §43. Minimal by design — V1 has few features —
but every rule in that section is enforced, not skipped.

## Authentication: dev-grade, explicitly not production-grade

`POST /users` mints an HMAC-SHA256-signed opaque token
(`internal/platform/auth`) at account creation. **There is no password and
no login flow.** The token encodes `{sub: user_id, region, tier, exp}`,
base64url-encoded, signed with a server-side secret (`AUTH_SECRET`):

```go
type Claims struct {
    UserID string `json:"sub"`
    Region string `json:"region"`
    Tier   string `json:"tier"`
    Exp    int64  `json:"exp"`
}
```

This exists so every other rule below ("never trust client-asserted
identity") can be enforced without building a full identity provider for a
V1 test platform. **Do not deploy this authentication scheme anywhere real
users' data would be at risk.** A production system would replace
`POST /users` + `internal/platform/auth` with a real IdP/OAuth flow and keep
everything downstream of "we have a verified `Identity`" unchanged — every
handler already reads identity from a verified `Claims`, never from a
request body field.

## Never trust the client

INSTRUCTIONS.md §43 lists `user_id`, `tier`, `region`, `permissions`. Every
one of these is read only from the verified token
(`identityFromContext` in `cmd/api/middleware.go`), never from a request
body or query parameter, including in the demo app — the demo's `region`
selector picks *which server to talk to*, not an identity claim; the server
still only trusts what's inside the signed token it issued.

## Authorization: channel membership

Every message-scoped operation checks membership before doing anything else:

- `POST /channels/{id}/messages` — `membershipRepo.IsMember`, 403 if false.
- `GET /channels/{id}/messages` — same check.
- `POST /channels/{id}/members` — the *actor* must already be a member
  (403 otherwise); there's no separate "channel owner" role in V1.

A user can never read or write a channel they haven't been added to, by
construction — there's no code path that skips the membership check.

## Input limits

- Request bodies capped at 64KB (`cmd/api/httpjson.go`,
  `maxRequestBody`), enforced via `http.MaxBytesReader`.
- Message bodies capped at 4000 characters (`handlers_messages.go`).
- Display names capped at 128 characters.
- JSON decoding uses `DisallowUnknownFields` — malformed/unexpected request
  shapes are rejected outright rather than silently ignored.

## Rate limiting and quotas

Every write path is behind `internal/quota` — see
[quotas-and-tiers.md](quotas-and-tiers.md). This is itself a security
control (resource exhaustion), not just a product feature.

## Transport

Local development runs plaintext HTTP/WS between docker-compose services on
an isolated bridge network — acceptable for a local multi-region
*simulation*. A real multi-region deployment needs TLS on every public
edge (`api`/`gateway` listeners) and either TLS or a private network for
inter-service traffic (cross-region forwarding, Postgres, Kafka, Redis)
per INSTRUCTIONS.md §43's "TLS" and "secure internal communication." This is
infrastructure/deployment configuration, not an application-code change —
nothing in the Go code assumes plaintext.

## What's explicitly out of scope for V1

Complex permissions (roles beyond "member"), E2EE, blocking. Per
INSTRUCTIONS.md §1 and §44 — these are additive features on top of the
membership + event-sourcing foundation already in place, not blocked by
anything built here.
