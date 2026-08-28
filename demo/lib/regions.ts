// Maps the three simulated logical regions to the local docker-compose port
// mappings (see deploy/docker-compose.yml). In a real deployment this would
// instead be "connect to the nearest edge" via geo-DNS/anycast — the demo
// hardcodes the choice so you can explicitly pick which region a browser tab
// "is in" and watch cross-region forwarding happen.
export type Region = "eu" | "us" | "asia";

export const REGIONS: Region[] = ["eu", "us", "asia"];

export const REGION_ENDPOINTS: Record<Region, { apiBase: string; wsBase: string; label: string }> = {
  eu: { apiBase: "http://localhost:8081", wsBase: "ws://localhost:8091", label: "EU (Frankfurt)" },
  us: { apiBase: "http://localhost:8082", wsBase: "ws://localhost:8092", label: "US (Virginia)" },
  asia: { apiBase: "http://localhost:8083", wsBase: "ws://localhost:8093", label: "Asia (Singapore)" },
};
