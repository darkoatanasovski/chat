"use client";

import { useEffect, useMemo, useState } from "react";
import { motion } from "framer-motion";
import { geoNaturalEarth1, geoPath } from "d3-geo";
import { feature } from "topojson-client";
import landTopology from "world-atlas/land-110m.json";
import { Globe2 } from "lucide-react";
import { getRegionUsage, ApiError } from "@/lib/api";
import type { RegionUsage } from "@/lib/types";
import { useSession } from "./shell";
import { ErrorBanner, Panel, Skeleton } from "./ui";

const WIDTH = 960;
const HEIGHT = 470;

const REGION_META: Record<string, { label: string; lon: number; lat: number; color: string }> = {
  eu: { label: "Europe", lon: 8.68, lat: 50.11, color: "var(--color-accent)" },
  us: { label: "North America", lon: -77.49, lat: 39.04, color: "var(--color-warning)" },
  asia: { label: "Asia Pacific", lon: 103.82, lat: 1.35, color: "var(--color-info)" },
};

const REGION_ORDER = ["eu", "us", "asia"];

function useMapGeometry() {
  return useMemo(() => {
    const landFeature = feature(landTopology as never, (landTopology as { objects: { land: unknown } }).objects.land as never) as GeoJSON.Feature;
    const projection = geoNaturalEarth1().fitSize([WIDTH, HEIGHT], landFeature);
    const pathGenerator = geoPath(projection);
    const landPath = pathGenerator(landFeature) ?? "";

    const markers = REGION_ORDER.map((region) => {
      const meta = REGION_META[region];
      const projected = projection([meta.lon, meta.lat]) ?? [0, 0];
      return { region, x: projected[0], y: projected[1], ...meta };
    });

    return { landPath, markers };
  }, []);
}

export function WorldMap() {
  const { session } = useSession();
  const { landPath, markers } = useMapGeometry();
  const [usage, setUsage] = useState<RegionUsage[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getRegionUsage(session.token)
      .then(setUsage)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }, [session.token]);

  const countByRegion = useMemo(() => {
    const map: Record<string, number> = {};
    usage?.forEach((r) => (map[r.region] = r.users));
    return map;
  }, [usage]);

  const totalUsers = usage?.reduce((sum, r) => sum + r.users, 0) ?? 0;

  return (
    <Panel animate={false} className="overflow-hidden">
      <div className="mb-5 flex items-center justify-between">
        <h2 className="flex items-center gap-2.5 text-base font-semibold text-text">
          <Globe2 className="h-5 w-5 text-text-muted" />
          End-users by region
        </h2>
        {usage && (
          <span className="font-mono text-sm text-text-faint">
            <span className="text-text">{totalUsers}</span> total
          </span>
        )}
      </div>

      {error && (
        <div className="mb-4">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      {!usage && !error && <Skeleton className="h-72 w-full" />}

      {usage && (
        <>
          <div className="relative w-full overflow-x-auto">
            <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} className="w-full min-w-[560px]" role="img" aria-label="World map of end-users by region">
              <defs>
                <filter id="marker-glow" x="-200%" y="-200%" width="500%" height="500%">
                  <feGaussianBlur stdDeviation="6" result="blur" />
                  <feMerge>
                    <feMergeNode in="blur" />
                    <feMergeNode in="SourceGraphic" />
                  </feMerge>
                </filter>
              </defs>

              <path d={landPath} fill="var(--color-surface-2)" stroke="var(--color-border)" strokeWidth={0.75} />

              {/* Faint arcs connecting the three hubs, ambient "network" motion. */}
              {markers.map((from, i) =>
                markers.slice(i + 1).map((to) => {
                  const midX = (from.x + to.x) / 2;
                  const midY = Math.min(from.y, to.y) - 46;
                  const d = `M ${from.x} ${from.y} Q ${midX} ${midY} ${to.x} ${to.y}`;
                  return (
                    <motion.path
                      key={`${from.region}-${to.region}`}
                      d={d}
                      fill="none"
                      stroke="var(--color-accent)"
                      strokeWidth={1}
                      strokeDasharray="1 7"
                      strokeLinecap="round"
                      initial={{ opacity: 0.15, strokeDashoffset: 0 }}
                      animate={{ strokeDashoffset: -64 }}
                      transition={{ duration: 6, ease: "linear", repeat: Infinity }}
                    />
                  );
                })
              )}

              {markers.map((marker, i) => {
                const count = countByRegion[marker.region] ?? 0;
                const radius = 4 + Math.min(15, Math.sqrt(count) * 3.2);
                const active = count > 0;
                return (
                  <g key={marker.region}>
                    {active && (
                      <motion.circle
                        cx={marker.x}
                        cy={marker.y}
                        r={radius}
                        fill="none"
                        stroke={marker.color}
                        strokeWidth={1.5}
                        initial={{ scale: 0.7, opacity: 0.6 }}
                        animate={{ scale: 2.6, opacity: 0 }}
                        transition={{ duration: 2.4, repeat: Infinity, ease: "easeOut", delay: i * 0.5 }}
                        style={{ transformOrigin: `${marker.x}px ${marker.y}px` }}
                      />
                    )}
                    <motion.circle
                      cx={marker.x}
                      cy={marker.y}
                      r={radius}
                      fill={marker.color}
                      filter="url(#marker-glow)"
                      opacity={active ? 0.95 : 0.35}
                      initial={{ scale: 0 }}
                      animate={{ scale: 1 }}
                      transition={{ delay: 0.2 + i * 0.1, type: "spring", stiffness: 300, damping: 18 }}
                      style={{ transformOrigin: `${marker.x}px ${marker.y}px` }}
                    />
                  </g>
                );
              })}
            </svg>
          </div>

          <div className="mt-3 flex flex-wrap items-center gap-6 border-t border-border-soft pt-5">
            {markers.map((marker) => (
              <div key={marker.region} className="flex items-center gap-2.5">
                <span className="h-3 w-3 rounded-full" style={{ backgroundColor: marker.color }} />
                <span className="text-[15px] text-text-muted">{marker.label}</span>
                <span className="font-mono text-[15px] text-text">{countByRegion[marker.region] ?? 0}</span>
              </div>
            ))}
          </div>
        </>
      )}
    </Panel>
  );
}
