// One WebSocket to the gateway per Playground actor. Deliberately a small
// hand-rolled client rather than importing @chat-platform/sdk/realtime: the
// console isn't a consumer of the published SDK package, and the
// Playground wants every frame verbatim (unknown types included) for its
// event feed, where the SDK's typed onEvent would narrow them.

export interface RealtimeFrame {
  type: string;
  [key: string]: unknown;
}

export type SocketStatus = "connecting" | "open" | "closed";

export interface PlaygroundSocketOptions {
  wsBase: string;
  token: string;
  onFrame: (frame: RealtimeFrame) => void;
  onStatus: (status: SocketStatus) => void;
}

const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 10_000;

export class PlaygroundSocket {
  #opts: PlaygroundSocketOptions;
  #ws: WebSocket | null = null;
  #closed = false;
  #attempt = 0;
  #timer: ReturnType<typeof setTimeout> | null = null;

  constructor(opts: PlaygroundSocketOptions) {
    this.#opts = opts;
    this.#connect();
  }

  #connect() {
    this.#opts.onStatus("connecting");
    const url = `${this.#opts.wsBase.replace(/\/$/, "")}/connect?token=${encodeURIComponent(this.#opts.token)}`;
    const ws = new WebSocket(url);
    ws.onopen = () => {
      this.#attempt = 0;
      this.#opts.onStatus("open");
    };
    ws.onmessage = (ev) => {
      try {
        const frame = JSON.parse(String(ev.data)) as RealtimeFrame;
        if (frame && typeof frame.type === "string") this.#opts.onFrame(frame);
      } catch {
        // Malformed frame — ignore, same tolerance the gateway has inbound.
      }
    };
    ws.onclose = () => {
      this.#opts.onStatus("closed");
      if (!this.#closed) this.#scheduleReconnect();
    };
    ws.onerror = () => {
      // onclose always follows; reconnect is handled there.
    };
    this.#ws = ws;
  }

  #scheduleReconnect() {
    this.#attempt += 1;
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** (this.#attempt - 1), RECONNECT_MAX_MS);
    this.#timer = setTimeout(() => this.#connect(), delay);
  }

  get isOpen() {
    return this.#ws?.readyState === WebSocket.OPEN;
  }

  /** Sends a frame up the socket. Returns false (and sends nothing) if the
   * socket isn't open right now — typing presence is best-effort, so the
   * caller just reports that rather than queueing. */
  send(frame: Record<string, unknown>): boolean {
    if (!this.isOpen) return false;
    this.#ws?.send(JSON.stringify(frame));
    return true;
  }

  close() {
    this.#closed = true;
    if (this.#timer) clearTimeout(this.#timer);
    this.#ws?.close();
  }
}
