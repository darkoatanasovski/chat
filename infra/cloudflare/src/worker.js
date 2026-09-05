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

    // CORS preflight must be answered at the edge BEFORE any auth/routing:
    // preflight carries no credentials, so it can't be resolved to a cell,
    // and the browser needs the CORS headers to allow the real request.
    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: corsHeaders(request) });
    }

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

    const kvKey = await resolvePlacementKey(request, url, env);
    if (!kvKey) {
      return jsonError(401, "could not resolve app for request; provide a valid api_key or bearer token");
    }

    let placement;
    try {
      placement = await env.PLACEMENT.get(kvKey, { type: "json" });
    } catch (e) {
      return jsonError(502, "placement lookup failed");
    }
    // Read-through: on a KV miss ask the control plane and cache the result,
    // so a freshly-created app routes immediately without a manual/cron sync.
    if (!placement || !placement.region) {
      placement = await readThroughPlacement(kvKey, env);
      if (placement && placement.region) {
        ctx.waitUntil(env.PLACEMENT.put(kvKey, JSON.stringify(placement)));
      }
    }
    if (!placement || !placement.region) {
      return jsonError(401, "unknown or unplaced api_key");
    }

    // WebSocket upgrades go to the region's ws origin; everything else to its
    // api origin.
    const isWs = (request.headers.get("Upgrade") || "").toLowerCase() === "websocket";
    const origin = (isWs ? parseJSON(env.WS_REGIONS) : parseRegions(env))[placement.region];
    if (!origin) {
      return jsonError(502, `no ${isWs ? "ws " : ""}origin configured for region ${placement.region}`, request);
    }

    const resp = await proxy(request, url, origin);
    recordAnalytics(env, { plane: isWs ? "ws" : "data", region: placement.region, shard: placement.shard || "-", status: resp.status });
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
  const pk = await resolvePlacementKey(request, url, env);
  if (!pk) {
    return jsonError(401, "upload requires a valid api_key or bearer token");
  }
  const ext = extensionFor(request.headers.get("content-type"));
  const key = `${pk.replace(/[^A-Za-z0-9_-]/g, "_")}/${crypto.randomUUID()}${ext}`;
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

// resolvePlacementKey returns the KV key to look the placement up under, or
// null. ?api_key= and an app token's api_key claim resolve to `apikey:<key>`;
// a user token's app_id claim resolves to `appid:<id>`. sync-kv.sh writes both
// kinds of entries, so the returned key matches an entry directly.
async function resolvePlacementKey(request, url, env) {
  const q = url.searchParams.get("api_key");
  if (q) return "apikey:" + q;

  // WebSocket connects can't set headers, so the user token is a query param
  // (?token=) — matches the ws /connect handler.
  const qt = url.searchParams.get("token");
  const auth = request.headers.get("Authorization") || "";
  const token = qt || (auth.startsWith("Bearer ") ? auth.slice("Bearer ".length) : "");
  if (!token) return null;

  const claims = await readClaims(token, env);
  if (!claims) return null;
  if (claims.api_key) return "apikey:" + claims.api_key;
  if (claims.app_id) return "appid:" + claims.app_id;
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

// readThroughPlacement resolves a placement from the control plane on a KV
// miss (GET CONTROL_ORIGIN/internal/placement?api_key=|app_id=). kvKey is
// "apikey:<key>" or "appid:<id>". Returns {region,shard} or null.
async function readThroughPlacement(kvKey, env) {
  if (!env.CONTROL_ORIGIN) return null;
  let query;
  if (kvKey.startsWith("apikey:")) query = "api_key=" + encodeURIComponent(kvKey.slice(7));
  else if (kvKey.startsWith("appid:")) query = "app_id=" + encodeURIComponent(kvKey.slice(6));
  else return null;
  try {
    const headers = env.INTERNAL_KEY ? { "X-Internal-Key": env.INTERNAL_KEY } : {};
    const resp = await fetch(`${env.CONTROL_ORIGIN}/internal/placement?${query}`, { headers });
    if (!resp.ok) return null;
    const p = await resp.json();
    return p && p.region ? p : null;
  } catch {
    return null;
  }
}

function parseJSON(s) {
  try {
    return JSON.parse(s || "{}");
  } catch {
    return {};
  }
}

function parseRegions(env) {
  return parseJSON(env.REGIONS);
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

// corsHeaders reflects the request Origin (works with or without credentials)
// and the requested headers, so both preflight and Worker-origin error
// responses are readable by the browser. Proxied responses keep the origin's
// own CORS headers.
function corsHeaders(request) {
  return {
    "access-control-allow-origin": request.headers.get("Origin") || "*",
    "access-control-allow-methods": "GET,POST,PUT,PATCH,DELETE,OPTIONS",
    "access-control-allow-headers":
      request.headers.get("Access-Control-Request-Headers") || "Content-Type,Authorization,X-Requested-With",
    "access-control-allow-credentials": "true",
    "access-control-max-age": "86400",
    vary: "Origin",
  };
}

function jsonError(status, message, request) {
  const headers = { "content-type": "application/json" };
  if (request) Object.assign(headers, corsHeaders(request));
  else headers["access-control-allow-origin"] = "*";
  return new Response(JSON.stringify({ error: message }), { status, headers });
}
