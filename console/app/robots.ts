import type { MetadataRoute } from "next";

const SITE_URL = process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3001";

// Everything under the console proper (overview, apps, billing, ...) is a
// signed-in area with nothing for a crawler to index and every page behind
// a client-side auth redirect anyway — only "/" (the marketing page) is
// worth indexing.
export default function robots(): MetadataRoute.Robots {
  return {
    rules: {
      userAgent: "*",
      allow: "/",
      disallow: "/console",
    },
    sitemap: `${SITE_URL}/sitemap.xml`,
  };
}
