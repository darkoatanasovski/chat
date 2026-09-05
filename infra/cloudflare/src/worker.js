// Cloudflare Worker edge router — the production alternative to cmd/router
// (docs/adr/0006-cell-based-tenant-routing.md, infra/README.md).
//
// Same routing contract as the Go router, at Cloudflare's edge:
//   1. Control-plane paths (/organizations, /apps, /dashboard, /dodo) proxy
//      to the control service (CONTROL_ORIGIN).
//   2. Everything else is resolved by apikey (?api_key= or a bearer token
//      claim) to a {region, shard} placement read from KV, then proxied to
//      that region's origin. WebSocket upgrades pass straight through.
//
// KV holds the placement map (populated by infra/cloudflare/sync-kv.sh from
// the config DB): key `apikey:<key>` -> {"region":"...","shard":"..."}. This
// is the edge equivalent of internal/appconfig's cache-first lookup — the
// config DB stays the source of truth; the sync keeps KV eventually current
// (and re-runs on any placement/credential change).
//
// Bindings (see wrangler.toml):
//   PLACEMENT     KV namespace: apikey -> placement
//   REGIONS       var: JSON object {"<region>":"https://<origin>", ...}
//   CONTROL_ORIGIN var: base URL of the control service
//   AUTH_SECRET   secret (optional): if set, bearer tokens are HMAC-verified
//                 at the edge; if unset, claims are read without verifying
//                 (the origin re-verifies regardless).

const CONTROL_PREFIXES = ["/organizations", "/apps", "/dashboard", "/dodo"];

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);

    if (url.pathname === "/healthz") {
      return new Response("ok", { status: 200 });
    }

    // Attachments upload → R2 (out of the box). Authenticated the same way as
    // data requests; the object URL is returned for the client to attach to a
    // message. Reads are served directly from R2's custom domain, off the
    // cell data path.
    if (url.pathname === "/uploads" && request.method === "POST") {
      return handleUpload(request, env);
    }

    if (isControlPath(url.pathname)) {
      if (!env.CONTROL_ORIGIN) {
        return jsonError(404, "control plane not configured");
      }
      const resp = await proxy(request, url, env.CONTROL_ORIGIN);
      recordAnalytics(env, { plane: "control", region: "-", shard: "-", status: resp.status });
      return resp;
    }

    const apiKey = await resolveApiKey(request, url, env);
    if (!apiKey) {
      return jsonError(401, "could not resolve app for request; provide a valid api_key or bearer token");
    }

    let placement;
    try {
      placement = await env.PLACEMENT.get(`apikey:${apiKey}`, { type: "json" });
    } catch (e) {
      return jsonError(502, "placement lookup failed");
    }
    if (!placement || !placement.region) {
      return jsonError(401, "unknown or unplaced api_key");
    }

    const regions = parseRegions(env);
    const origin = regions[placement.region];
    if (!origin) {
      return jsonError(502, `no origin configured for region ${placement.region}`);
    }

    const resp = await proxy(request, url, origin);
    recordAnalytics(env, { plane: "data", region: placement.region, shard: placement.shard || "-", status: resp.status });
    return resp;
  },

  // Cron Trigger: refresh the placement KV from PLACEMENT_SYNC_URL so the edge
  // self-heals even if an on-change sync was missed. The source returns
  // [{key, region, shard}]; writes go through the KV binding (no API token).
  async scheduled(event, env, ctx) {
    if (!env.PLACEMENT_SYNC_URL) return;
    ctx.waitUntil(refreshPlacements(env));
  },
};

async function refreshPlacements(env) {
  const resp = await fetch(env.PLACEMENT_SYNC_URL);
  if (!resp.ok) return;
  const rows = await resp.json();
  await Promise.all(
    rows.map((r) => env.PLACEMENT.put(r.key, JSON.stringify({ region: r.region, shard: r.shard })))
  );
}

// recordAnalytics writes one edge datapoint per request when the Analytics
// Engine dataset is bound. Blobs are indexed dimensions; a double carries the
// count. No-op if the binding isn't configured.
function recordAnalytics(env, { plane, region, shard, status }) {
  if (!env.EDGE_ANALYTICS) return;
  try {
    env.EDGE_ANALYTICS.writeDataPoint({
      blobs: [plane, region, shard, String(status)],
      doubles: [1],
      indexes: [region],
    });
  } catch {
    /* analytics is best-effort, never fail a request over it */
  }
}

// handleUpload stores a request body in R2 and returns its public URL. It
// requires a valid apikey/token (same resolution as data requests) so uploads
// can't be anonymous, and namespaces objects by app to keep tenants separate.
async function handleUpload(request, env) {
  if (!env.ATTACHMENTS) {
    return jsonError(404, "attachments (R2) not configured");
  }
  const url = new URL(request.url);
  const apiKey = await resolveApiKey(request, url, env);
  if (!apiKey) {
    return jsonError(401, "upload requires a valid api_key or bearer token");
  }
  const ext = extensionFor(request.headers.get("content-type"));
  const key = `${apiKey.replace(/[^A-Za-z0-9_-]/g, "_")}/${crypto.randomUUID()}${ext}`;
  try {
    await env.ATTACHMENTS.put(key, request.body, {
      httpMetadata: { contentType: request.headers.get("content-type") || "application/octet-stream" },
    });
  } catch {
    return jsonError(502, "upload failed");
  }
  const base = (env.ATTACHMENTS_BASE_URL || "").replace(/\/$/, "");
  return Response.json({ url: base ? `${base}/${key}` : key, key });
}

function extensionFor(contentType) {
  const map = { "image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "application/pdf": ".pdf" };
  return map[(contentType || "").split(";")[0].trim()] || "";
}

function isControlPath(path) {
  return CONTROL_PREFIXES.some((p) => path === p || path.startsWith(p + "/"));
}

// resolveApiKey mirrors the Go router: ?api_key= wins (browser WebSockets
// can't set headers), else a bearer token's api_key (app token) or app_id
// (user token) claim. app_id is returned as the KV key `appid:<n>` — the sync
// writes both apikey:* and appid:* entries.
async function resolveApiKey(request, url, env) {
  const q = url.searchParams.get("api_key");
  if (q) return q;

  const auth = request.headers.get("Authorization") || "";
  if (!auth.startsWith("Bearer ")) return null;
  const token = auth.slice("Bearer ".length);

  const claims = await readClaims(token, env);
  if (!claims) return null;
  if (claims.api_key) return claims.api_key;
  if (claims.app_id) return `appid:${claims.app_id}`; // resolved by an appid:* KV entry
  return null;
}

// readClaims decodes (and, when AUTH_SECRET is set, verifies) the platform
// token: base64url(json) + "." + base64url(HMAC-SHA256). Matches
// internal/platform/auth/token.go.
async function readClaims(token, env) {
  const dot = token.indexOf(".");
  if (dot < 0) return null;
  const payloadB64 = token.slice(0, dot);
  const sigB64 = token.slice(dot + 1);

  if (env.AUTH_SECRET) {
    const ok = await verifyHMAC(payloadB64, sigB64, env.AUTH_SECRET);
    if (!ok) return null;
  }
  try {
    return JSON.parse(new TextDecoder().decode(b64urlToBytes(payloadB64)));
  } catch {
    return null;
  }
}

async function verifyHMAC(payloadB64, sigB64, secret) {
  try {
    const key = await crypto.subtle.importKey(
      "raw",
      new TextEncoder().encode(secret),
      { name: "HMAC", hash: "SHA-256" },
      false,
      ["verify"]
    );
    return await crypto.subtle.verify("HMAC", key, b64urlToBytes(sigB64), new TextEncoder().encode(payloadB64));
  } catch {
    return false;
  }
}

function b64urlToBytes(s) {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=");
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

function parseRegions(env) {
  try {
    return JSON.parse(env.REGIONS || "{}");
  } catch {
    return {};
  }
}

// proxy forwards the request to `origin` keeping the path, query, method,
// headers and body. Passing the original Request through preserves WebSocket
// upgrades (Cloudflare tunnels them end to end).
function proxy(request, url, origin) {
  const target = new URL(origin);
  target.pathname = url.pathname;
  target.search = url.search;
  return fetch(new Request(target.toString(), request));
}

function jsonError(status, message) {
  return new Response(JSON.stringify({ error: message }), {
    status,
    headers: { "content-type": "application/json" },
  });
}
