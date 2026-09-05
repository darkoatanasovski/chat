// Durable Objects realtime — an EXPERIMENTAL edge-native alternative to the
// ws + Kafka-per-cell fanout (docs/adr/0006-cell-based-tenant-routing.md,
// infra/cloudflare/cloudflare-services.md).
//
// One Durable Object per channel (`ChannelRoom`) is a single-threaded actor
// that owns that channel's live connections: it terminates WebSockets at the
// edge, fans a new message out to every connected member, and tracks presence
// — the job internal/realtime does today with a hub + Redis pub/sub + a Kafka
// consumer group. Because a DO is globally addressable by name
// (idFromName(channel_id)), every member of a channel lands on the SAME object
// regardless of region, so delivery is ordered with no cross-instance routing.
//
// This is a scaffold to evaluate against the Kafka path, NOT a drop-in
// replacement yet: it does not persist messages, enforce membership/blocks,
// or assign sequence numbers (all of which the cell's api/Postgres still own).
// Wiring would be: the cell api, after committing a message, POSTs it to
// /broadcast (this worker) instead of / in addition to the outbox→Kafka fanout.

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const channel = url.searchParams.get("channel");
    if (!channel) return new Response("channel required", { status: 400 });

    // All traffic for a channel is routed to that channel's single DO.
    const id = env.CHANNEL.idFromName(channel);
    const stub = env.CHANNEL.get(id);
    return stub.fetch(request);
  },
};

export class ChannelRoom {
  constructor(state, env) {
    this.state = state;
    this.env = env;
  }

  async fetch(request) {
    const url = new URL(request.url);

    // Client connects a WebSocket: GET /connect?channel=<id>
    if (url.pathname === "/connect") {
      if (request.headers.get("Upgrade") !== "websocket") {
        return new Response("expected websocket", { status: 426 });
      }
      const pair = new WebSocketPair();
      const [client, server] = Object.values(pair);
      // Hibernation API: the DO can evict from memory between messages and the
      // socket survives — cheap to hold many idle channels open.
      this.state.acceptWebSocket(server);
      return new Response(null, { status: 101, webSocket: client });
    }

    // The cell api posts a committed message here to fan it out: POST /broadcast
    if (url.pathname === "/broadcast" && request.method === "POST") {
      const payload = await request.text();
      for (const ws of this.state.getWebSockets()) {
        try {
          ws.send(payload);
        } catch {
          /* a dead socket is cleaned up by webSocketClose */
        }
      }
      return new Response(null, { status: 204 });
    }

    return new Response("not found", { status: 404 });
  }

  // Relay client→channel frames (e.g. typing) to the other connections. Real
  // message sends still go through the cell api (persistence + sequence);
  // this is only the live relay.
  async webSocketMessage(ws, message) {
    for (const other of this.state.getWebSockets()) {
      if (other !== ws) {
        try {
          other.send(message);
        } catch {
          /* ignore */
        }
      }
    }
  }

  async webSocketClose(ws, code, reason, wasClean) {
    try {
      ws.close(code, reason);
    } catch {
      /* already closed */
    }
  }
}
