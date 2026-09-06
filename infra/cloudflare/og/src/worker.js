// OpenGraph link-preview service on Cloudflare Workers — the edge-native
// alternative to the Go cmd/ogservice. Given GET /og?url=<target>, it fetches
// the page and extracts OpenGraph/Twitter/fallback metadata using HTMLRewriter
// (Cloudflare's built-in streaming HTML parser — no DOM library, no browser),
// caches the result at the edge, and returns the same JSON shape cmd/api's
// fetchLinkPreview already expects: {url,title,description,image_url,site_name}.
//
// The cell api points OG_SERVICE_URL here; nothing else changes. A 502 means
// the target fetch itself failed (api treats that as "no preview"), matching
// the Go service's contract.

const FETCH_TIMEOUT_MS = 5000;
const CACHE_TTL_S = 3600;

export default {
  async fetch(request) {
    const url = new URL(request.url);
    if (url.pathname === "/healthz") return new Response("ok");
    if (url.pathname !== "/og") return json(404, { error: "not found" });

    const target = url.searchParams.get("url");
    if (!target || !isFetchableUrl(target)) {
      return json(400, { error: "valid http(s) url required" });
    }

    // Edge cache keyed by the normalized target.
    const cacheKey = new Request("https://og.cache/" + encodeURIComponent(target));
    const cache = caches.default;
    const cached = await cache.match(cacheKey);
    if (cached) return cached;

    let page;
    try {
      page = await fetch(target, {
        redirect: "follow",
        signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
        headers: {
          "user-agent": "chat-og-bot/1.0 (+link preview)",
          accept: "text/html,application/xhtml+xml",
        },
      });
    } catch {
      return json(502, { error: "fetch failed" }); // api: no preview
    }
    if (!page.ok) return json(502, { error: "target returned " + page.status });
    const ct = page.headers.get("content-type") || "";
    if (!ct.includes("html")) {
      // Not an HTML page (image, pdf, …) — nothing to scrape.
      return finish(cache, cacheKey, { url: target });
    }

    const meta = await extractOG(page);
    const image = meta.image ? absolutize(meta.image, page.url || target) : "";
    const result = {
      url: target,
      title: meta.title || "",
      description: meta.description || "",
      image_url: image,
      site_name: meta.site_name || "",
    };
    return finish(cache, cacheKey, result);
  },
};

async function finish(cache, cacheKey, obj) {
  const resp = json(200, obj, { "cache-control": `public, max-age=${CACHE_TTL_S}` });
  try { await cache.put(cacheKey, resp.clone()); } catch { /* cache best-effort */ }
  return resp;
}

// extractOG streams the HTML through HTMLRewriter, collecting the first value
// seen for each field (og:* preferred, then twitter:*, then plain fallbacks).
async function extractOG(resp) {
  const og = {};
  const first = (k) => ({
    element(e) {
      const c = e.getAttribute("content");
      if (c && !og[k]) og[k] = c.trim();
    },
  });
  let titleText = "";
  const rewriter = new HTMLRewriter()
    .on('meta[property="og:title"]', first("title"))
    .on('meta[name="twitter:title"]', first("title"))
    .on('meta[property="og:description"]', first("description"))
    .on('meta[name="twitter:description"]', first("description"))
    .on('meta[name="description"]', first("description"))
    .on('meta[property="og:image"]', first("image"))
    .on('meta[property="og:image:url"]', first("image"))
    .on('meta[name="twitter:image"]', first("image"))
    .on('meta[property="og:site_name"]', first("site_name"))
    .on("title", { text(t) { titleText += t.text; } });

  await rewriter.transform(resp).arrayBuffer(); // drives the parser to completion
  if (!og.title && titleText.trim()) og.title = titleText.trim();
  return og;
}

function isFetchableUrl(u) {
  let parsed;
  try { parsed = new URL(u); } catch { return false; }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return false;
  const h = parsed.hostname.toLowerCase();
  // Basic SSRF guard: refuse obvious local/loopback/link-local targets.
  if (h === "localhost" || h === "0.0.0.0" || h.endsWith(".localhost")) return false;
  if (/^127\.|^10\.|^192\.168\.|^169\.254\.|^172\.(1[6-9]|2\d|3[01])\./.test(h)) return false;
  return true;
}

function absolutize(src, base) {
  try { return new URL(src, base).toString(); } catch { return src; }
}

function json(status, obj, extra = {}) {
  return new Response(JSON.stringify(obj), {
    status,
    headers: { "content-type": "application/json", ...extra },
  });
}
