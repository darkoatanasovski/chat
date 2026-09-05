import { getCloudflareContext } from "@opennextjs/cloudflare";

// POST /api/demo/session { username }
// Server-side: mints an end-user in the demo (ENTERPRISE) app and adds them to
// the shared Lobby channel, then returns a scoped user token to the browser.
// The demo app credentials + demo-org login live only here (Worker secrets),
// never in the client bundle.
export async function POST(request: Request): Promise<Response> {
  const { env } = getCloudflareContext();
  const e = env as Record<string, string>;
  // Call the Railway origins DIRECTLY (server-side): a Worker can't reliably
  // fetch another same-account Worker via workers.dev, so we don't route these
  // through the edge. The browser still uses the edge for the chat itself.
  const control = e.DEMO_CONTROL_ORIGIN;
  const api = e.DEMO_API_ORIGIN;
  const key = e.DEMO_APP_KEY;
  const secret = e.DEMO_APP_SECRET;
  const channelId = e.DEMO_CHANNEL_ID;
  const orgEmail = e.DEMO_ORG_EMAIL;
  const orgPassword = e.DEMO_ORG_PASSWORD;

  if (!control || !api || !key || !secret || !channelId) {
    return json(500, { error: "demo is not configured" });
  }

  let username = "";
  try {
    username = String(((await request.json()) as { username?: string }).username || "").trim();
  } catch {
    return json(400, { error: "invalid request" });
  }
  if (username.length < 1 || username.length > 40) {
    return json(400, { error: "username must be 1–40 characters" });
  }

  try {
    // 1. app key/secret -> short-lived app JWT (control plane)
    const appTok = await postJSON(control, "/apps/token", {
      headers: { authorization: "Basic " + btoa(`${key}:${secret}`) },
    });

    // 2. create the visitor end-user (data plane — the demo app's cell api)
    const user = await postJSON(api, "/users", {
      headers: { authorization: `Bearer ${appTok.token}` },
      body: { display_name: username },
    });

    // 3. add the visitor to the shared Lobby via the demo org's dashboard
    //    session (operator add-member bypasses the "must be a member" rule).
    if (orgEmail && orgPassword) {
      const session = await postJSON(control, "/dashboard/login", {
        body: { email: orgEmail, password: orgPassword },
      });
      await fetch(`${control}/dashboard/channels/${channelId}/members`, {
        method: "POST",
        headers: { authorization: `Bearer ${session.token}`, "content-type": "application/json" },
        body: JSON.stringify({ user_id: user.user_id }),
      });
    }

    return json(200, {
      token: user.token,
      userId: user.user_id,
      displayName: username,
      channelId,
    });
  } catch {
    return json(502, { error: "could not start demo session" });
  }
}

async function postJSON(
  base: string,
  path: string,
  opts: { headers?: Record<string, string>; body?: unknown },
): Promise<Record<string, string>> {
  const resp = await fetch(base + path, {
    method: "POST",
    headers: { "content-type": "application/json", ...(opts.headers || {}) },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });
  if (!resp.ok) throw new Error(`${path} -> ${resp.status}`);
  return resp.json();
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}
