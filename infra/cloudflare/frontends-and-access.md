# Frontends (Pages), Access, and Turnstile

Config-first Cloudflare adoption for the human-facing surfaces. The server-side
Turnstile check is already implemented (control plane); the rest is dashboard
setup plus a small frontend widget.

## Pages — host `console/` and `demo/`

Both are Next.js apps; deploy each as a Cloudflare Pages project (Git
integration, preview deploys per PR):

- **Build command:** `npm run build`
- **Framework preset:** Next.js
- **Root directory:** `console` (and a second project for `demo`)
- **Env:** point the app at the edge router, e.g. `NEXT_PUBLIC_API_BASE=https://api.chat.io`

Pages serves the static/edge-rendered frontend globally; all API/data calls go
to the Worker router (`api.chat.io`) as usual.

## Access (Zero Trust) — protect internal surfaces

Put operator/internal surfaces behind Cloudflare Access so they need SSO, not a
VPN or a shared password. Create an Access application per host:

| Application (hostname/path) | Policy |
|---|---|
| Grafana (`grafana.chat.io`) | Allow: your team's email domain / IdP group |
| The dashboard admin, if hosted on a CF domain | Allow: org operators |
| Any `/internal/*` export endpoints (e.g. a future placements dump for the KV cron) | Service Auth token (for the Worker) + team |

Access enforces at the edge before traffic reaches the origin; combine with WAF
+ Rate Limiting (see `cloudflare-services.md`).

## Turnstile — signup bot protection

Server side is **done**: `POST /dashboard/signup` verifies `turnstile_token`
when `TURNSTILE_SECRET` is set (`cmd/api/turnstile.go`); empty secret = disabled
so dev/tests are unaffected.

To turn it on:

1. Create a Turnstile widget (Cloudflare dashboard → Turnstile) for the console
   domain; note the **site key** (public) and **secret key**.
2. Set `TURNSTILE_SECRET` on the **control** service (it runs signup).
3. Add the widget to the console signup form and send its token as
   `turnstile_token`:

   ```html
   <div class="cf-turnstile" data-sitekey="YOUR_SITE_KEY"></div>
   <script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
   ```
   ```js
   // on submit
   body.turnstile_token = document.querySelector('[name="cf-turnstile-response"]').value;
   ```

With the secret unset, signup behaves exactly as before — this is purely
additive.
