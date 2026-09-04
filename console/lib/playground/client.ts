// The Playground's end-user-side HTTP layer. Unlike lib/api.ts (which talks
// to the dashboard routes as the signed-in org user), everything here runs
// as one of an app's END-USERS, authenticated with a client token minted by
// lib/api.ts's mintEndUserToken — the same routes a business's own client
// app would call with a token from POST /users.
//
// Requests are described as plain data (RequestSpec) rather than as
// functions so one description can drive three things at once: the actual
// fetch, the request/response log the Playground shows, and the code
// snippets (lib/playground/snippets.ts) that render the same call in cURL,
// fetch, and Python without a second per-feature template.
import { API_BASE } from "@/lib/api";

/** Gateway (WebSocket) base for the Playground's live event feed. Mirrors
 * the demo app's REGION_ENDPOINTS default for eu — any gateway works for
 * any end-user since delivery is forwarded cross-region. */
export const GATEWAY_BASE = process.env.NEXT_PUBLIC_GATEWAY_BASE ?? "ws://localhost:8091";

export { API_BASE };

export type HttpMethod = "GET" | "POST" | "PATCH" | "DELETE";

export interface HttpSpec {
  kind: "http";
  method: HttpMethod;
  /** Fully substituted path, e.g. "/channels/8f3…/messages". */
  path: string;
  query?: Record<string, string | number | undefined>;
  body?: unknown;
}

/** A frame pushed UP the realtime socket (typing presence is the only thing
 * the gateway accepts inbound today — see internal/realtime's inboundFrame). */
export interface WsSpec {
  kind: "ws";
  frame: Record<string, unknown>;
}

export type RequestSpec = HttpSpec | WsSpec;

/** One end-user the Playground can act as: an existing end-user of the
 * chosen app plus a dashboard-minted client token for them. */
export interface Actor {
  userId: string;
  displayName: string;
  region: string;
  token: string;
  /** RFC3339 — the Playground re-mints shortly before this passes. */
  expiresAt: string;
}

export interface RunResult {
  status: number;
  ok: boolean;
  response: unknown;
  durationMs: number;
}

export interface RequestRecord {
  id: number;
  at: number;
  actor: Pick<Actor, "userId" | "displayName">;
  spec: RequestSpec;
  /** Absent for a ws frame — there's no response to a typing frame, the
   * effect (if any) shows up in the event feed instead. */
  result?: RunResult;
  featureId: string;
}

export function buildUrl(apiBase: string, spec: HttpSpec): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(spec.query ?? {})) {
    if (v !== undefined && v !== "") params.set(k, String(v));
  }
  const qs = params.toString();
  return `${apiBase}${spec.path}${qs ? `?${qs}` : ""}`;
}

/** Executes an HttpSpec as the given end-user token. Never throws on a
 * non-2xx — the Playground's whole point is showing the real status and
 * error body (a 403 for a capability that's switched off is a result worth
 * seeing, not an exception to swallow). Only a network-level failure
 * rejects. */
export async function runHttp(apiBase: string, token: string, spec: HttpSpec): Promise<RunResult> {
  const started = performance.now();
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (spec.body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(buildUrl(apiBase, spec), {
    method: spec.method,
    headers,
    body: spec.body !== undefined ? JSON.stringify(spec.body) : undefined,
  });
  let response: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      response = JSON.parse(text);
    } catch {
      response = text;
    }
  }
  return { status: res.status, ok: res.ok, response, durationMs: Math.round(performance.now() - started) };
}

/** True when a token is expired or about to be (within `skewMs`) — the
 * Playground re-mints rather than letting a request race the expiry. */
export function tokenNeedsRefresh(actor: Actor, skewMs = 60_000): boolean {
  const exp = new Date(actor.expiresAt).getTime();
  return !Number.isFinite(exp) || exp - Date.now() < skewMs;
}
