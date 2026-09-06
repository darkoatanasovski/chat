// Durable Objects realtime — edge-native fan-out, an alternative transport to
// the ws + Kafka-per-cell path (docs/adr/0006-cell-based-tenant-routing.md).
//
// One Durable Object per channel (`ChannelRoom`) terminates member WebSockets
// at the nearest Cloudflare PoP and fans a frame out to them, cutting the
// origin-region round trip a Kafka-backed ws connection pays. The cell's
// api/Postgres still own persistence, sequence, membership and blocks — this is
// purely the live delivery hop.
//
// Trust model (production):
//   - GET  /connect?channel=&token=  authenticates by asking the ORIGIN
//     (API_ORIGIN + /channels/:id/access) with the caller's own user token, so
//     only a member's socket is ever accepted. No token secret lives here.
//   - POST /broadcast?channel=       is origin-only, gated by X-Internal-Key
//     (== INTERNAL_KEY, the ws service's INTERNAL_AUTH_KEY). Body carries the
//     resolved recipient set {to:[user_id...], frame:<event>}; the DO delivers
//     the frame only to those users' sockets, preserving the block/exclude
//     filtering the origin already computed.
//   - client→DO frames are relayed to peers ONLY when they are typing signals,
//     so a client can never inject a fake message/reaction to others.

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const channel = url.searchParams.get("channel");
    if (!channel) return json(400, { error: "channel required" });

    if (url.pathname === "/connect") {
      if (request.headers.get("Upgrade") !== "websocket") {
        return json(426, { error: "expected websocket" });
      }
      const token = url.searchParams.get("token");
      if (!token) return json(401, { error: "token required" });
      // Authorize against the origin: is this token's user a member of channel?
      const access = await authorize(env, channel, token);
      if (!access.ok) return json(access.status, { error: access.error });

      // Route to the channel's single DO, passing the resolved user id so the
      // DO can tag the socket for recipient-filtered delivery.
      const fwd = new URL(request.url);
      fwd.searchParams.set("uid", access.userId);
      fwd.searchParams.delete("token");
      const stub = env.CHANNEL.get(env.CHANNEL.idFromName(channel));
      return stub.fetch(new Request(fwd, request));
    }

    if (url.pathname === "/broadcast" && request.method === "POST") {
      if (!env.INTERNAL_KEY || request.headers.get("X-Internal-Key") !== env.INTERNAL_KEY) {
        return json(403, { error: "forbidden" });
      }
      const stub = env.CHANNEL.get(env.CHANNEL.idFromName(channel));
      return stub.fetch(request);
    }

    return json(404, { error: "not found" });
  },
};

// authorize asks the origin whether token's user may join channel. Returns
// {ok, userId} or {ok:false, status, error}. All token verification (signature,
// expiry) is the origin's — the worker holds no signing secret.
async function authorize(env, channel, token) {
  if (!env.API_ORIGIN) return { ok: false, status: 500, error: "realtime not configured" };
  let resp;
  try {
    resp = await fetch(`${env.API_ORIGIN}/channels/${encodeURIComponent(channel)}/access`, {
      headers: { authorization: "Bearer " + token },
    });
  } catch {
    return { ok: false, status: 502, error: "auth upstream unreachable" };
  }
  if (resp.status === 401) return { ok: false, status: 401, error: "invalid token" };
  if (resp.status === 403) return { ok: false, status: 403, error: "not a member of this channel" };
  if (!resp.ok) return { ok: false, status: 502, error: "auth failed" };
  const body = await resp.json().catch(() => ({}));
  if (!body.user_id) return { ok: false, status: 502, error: "auth malformed" };
  return { ok: true, userId: String(body.user_id) };
}

function json(status, obj) {
  return new Response(JSON.stringify(obj), { status, headers: { "content-type": "application/json" } });
}

export class ChannelRoom {
  constructor(state, env) {
    this.state = state;
    this.env = env;
  }

  async fetch(request) {
    const url = new URL(request.url);

    if (url.pathname === "/connect") {
      const pair = new WebSocketPair();
      const [client, server] = Object.values(pair);
      const uid = url.searchParams.get("uid") || "";
      // Hibernation API with the user id as a tag, so /broadcast can address a
      // specific recipient's socket(s) via getWebSockets(uid).
      this.state.acceptWebSocket(server, uid ? [uid] : []);
      return new Response(null, { status: 101, webSocket: client });
    }

    if (url.pathname === "/broadcast" && request.method === "POST") {
      let msg;
      try {
        msg = await request.json();
      } catch {
        return new Response("bad body", { status: 400 });
      }
      const frame = typeof msg.frame === "string" ? msg.frame : JSON.stringify(msg.frame);
      const to = Array.isArray(msg.to) ? msg.to : null;
      if (to) {
        // Deliver only to the resolved recipients' sockets.
        const seen = new Set();
        for (const uid of to) {
          for (const ws of this.state.getWebSockets(String(uid))) {
            if (seen.has(ws)) continue;
            seen.add(ws);
            try { ws.send(frame); } catch { /* dead socket cleaned on close */ }
          }
        }
      } else {
        // No recipient list → whole room (used for non-filtered events).
        for (const ws of this.state.getWebSockets()) {
          try { ws.send(frame); } catch { /* ignore */ }
        }
      }
      return new Response(null, { status: 204 });
    }

    return new Response("not found", { status: 404 });
  }

  // Client→channel frames: relay ONLY typing signals to peers. Real events come
  // from the origin via /broadcast; a client can't inject anything else here.
  async webSocketMessage(ws, message) {
    let f;
    try {
      f = JSON.parse(typeof message === "string" ? message : new TextDecoder().decode(message));
    } catch {
      return;
    }
    if (typeof f.type !== "string" || !f.type.startsWith("typing")) return;
    const payload = JSON.stringify(f);
    for (const other of this.state.getWebSockets()) {
      if (other !== ws) {
        try { other.send(payload); } catch { /* ignore */ }
      }
    }
  }

  async webSocketClose(ws, code, reason) {
    try { ws.close(code, reason); } catch { /* already closed */ }
  }
}
