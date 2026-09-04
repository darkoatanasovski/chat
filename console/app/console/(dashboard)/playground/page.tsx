"use client";

// Playground — a global console page (not per-app) for trying every
// end-user feature of the platform against a real app: pick an app, mint
// tokens for a couple of its end-users ("actors"), pick a channel, and run
// any feature from the catalog as one of them while watching what the
// others receive over their realtime connections. Every run also renders
// as a copy-pasteable snippet (SDK / fetch / cURL / Python).
//
// The catalog itself lives in lib/playground/features.ts; per-app state in
// components/playground/use-playground.ts.
import { Suspense, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Boxes, FlaskConical } from "lucide-react";
import { ApiError } from "@/lib/api";
import { useAppsQuery, useChannelMembersQuery } from "@/lib/queries";
import { defaultValues, FEATURE_GROUPS, FEATURES, featureById, type Values } from "@/lib/playground/features";
import { useSession } from "@/components/shell";
import { Button, ErrorBanner, Panel, Skeleton, cx } from "@/components/ui";
import { FeaturePanel, type UserOption } from "@/components/playground/feature-panel";
import { LivePanel } from "@/components/playground/live-panel";
import { SetupBar } from "@/components/playground/setup-bar";
import { usePlayground } from "@/components/playground/use-playground";

const APP_KEY = "chat-console:playground:app";
const FEATURE_KEY = "chat-console:playground:feature";
const DEFAULT_FEATURE = "message.send";

export default function PlaygroundPage() {
  return (
      <Suspense fallback={null}>
        <PlaygroundView />
      </Suspense>
  );
}

function PlaygroundView() {
  const { session } = useSession();
  const searchParams = useSearchParams();
  const appsQuery = useAppsQuery(session.token, session.org.org_id);
  const apps = appsQuery.data ?? null;
  const error = appsQuery.error ? (appsQuery.error instanceof ApiError ? appsQuery.error.message : String(appsQuery.error)) : null;

  const [appId, setAppIdState] = useState<number | null>(null);
  const [featureId, setFeatureIdState] = useState<string>(DEFAULT_FEATURE);
  const [valuesByFeature, setValuesByFeature] = useState<Record<string, Values>>({});

  // Resolve which app to start on: ?app= (deep link) > remembered > first.
  useEffect(() => {
    if (!apps || apps.length === 0) return;
    const fromUrl = Number(searchParams.get("app"));
    let stored: number | null = null;
    try {
      stored = Number(window.localStorage.getItem(APP_KEY)) || null;
    } catch {
      // ignore
    }
    const candidate = [fromUrl, stored].find((id) => id && apps.some((a) => a.app_id === id)) ?? apps[0].app_id;
    setAppIdState((current) => (current && apps.some((a) => a.app_id === current) ? current : candidate));
    try {
      const storedFeature = window.localStorage.getItem(FEATURE_KEY);
      if (storedFeature && featureById(storedFeature)) setFeatureIdState(storedFeature);
    } catch {
      // ignore
    }
  }, [apps, searchParams]);

  function setAppId(id: number) {
    setAppIdState(id);
    try {
      window.localStorage.setItem(APP_KEY, String(id));
    } catch {
      // ignore
    }
  }

  function setFeatureId(id: string) {
    setFeatureIdState(id);
    try {
      window.localStorage.setItem(FEATURE_KEY, id);
    } catch {
      // ignore
    }
  }

  const app = apps?.find((a) => a.app_id === appId) ?? null;
  const playground = usePlayground(session.token, appId);
  const feature = featureById(featureId) ?? FEATURES[0];
  const values = valuesByFeature[feature.id] ?? defaultValues(feature);

  // Channel members as "user" field options (alongside the actors), named
  // from the dashboard's member list so ids read as people.
  const membersQuery = useChannelMembersQuery(session.token, playground.channelId ?? undefined, { enabled: !!playground.channelId });
  const userOptions = useMemo<UserOption[]>(() => {
    const seen = new Map<string, string>();
    for (const a of playground.actors) seen.set(a.userId, `${a.displayName} (actor)`);
    for (const m of membersQuery.data ?? []) if (!seen.has(m.user_id)) seen.set(m.user_id, `${m.display_name} (member)`);
    return Array.from(seen, ([userId, label]) => ({ userId, label }));
  }, [playground.actors, membersQuery.data]);

  const nameOf = (userId: string) => {
    const actor = playground.actorName(userId);
    if (actor) return actor;
    const member = membersQuery.data?.find((m) => m.user_id === userId);
    return member?.display_name ?? (userId.length > 12 ? `${userId.slice(0, 8)}…` : userId);
  };

  const ctx = playground.activeActor
    ? playground.context(playground.activeActor)
    : { apiBase: "", wsBase: "", channelId: playground.channelId ?? "", actor: { userId: "", displayName: "" }, recent: playground.recent };
  // Snippets should still render sensibly before an actor exists.
  if (!playground.activeActor) {
    ctx.apiBase = process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8081";
    ctx.wsBase = process.env.NEXT_PUBLIC_GATEWAY_BASE ?? "ws://localhost:8091";
  }

  const lastRecord = playground.requests.find((r) => r.featureId === feature.id) ?? null;

  async function run() {
    const record = await playground.run(feature, values);
    // Creating a channel from the catalog should select it, same as the
    // setup bar's own "New" does.
    if (record?.result?.ok && feature.id === "channel.create") {
      const created = record.result.response as { channel_id?: string } | null;
      if (created?.channel_id) playground.setChannelId(created.channel_id);
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-text">Playground</h1>
          <p className="mt-1.5 text-[15px] text-text-muted">
            Try every end-user feature against one of your apps — as real end-users, over the real API — and grab the code for it.
          </p>
        </div>
      </div>

      {error && <ErrorBanner>{error}</ErrorBanner>}

      {apps === null && !error && <Skeleton className="h-40" />}

      {apps?.length === 0 && (
        <Panel className="flex flex-col items-center gap-4 py-16 text-center">
          <span className="grid h-14 w-14 place-items-center rounded-full bg-surface-2 text-text-faint">
            <Boxes className="h-6 w-6" />
          </span>
          <p className="text-[15px] text-text-muted">The Playground needs an app to act inside — create your first one.</p>
          <Link href="/console/apps">
            <Button variant="primary">Go to Apps</Button>
          </Link>
        </Panel>
      )}

      {apps && apps.length > 0 && (
        <>
          <SetupBar apps={apps} appId={appId} onAppChange={setAppId} playground={playground} />

          <div className="grid items-start gap-5 xl:grid-cols-[200px_minmax(0,1fr)_360px]">
            <nav className="flex flex-col gap-4 xl:sticky xl:top-0">
              {FEATURE_GROUPS.map((group) => {
                const items = FEATURES.filter((f) => f.group === group);
                if (items.length === 0) return null;
                return (
                  <div key={group}>
                    <div className="mb-1 px-2 text-[11px] font-semibold uppercase tracking-wide text-text-faint">{group}</div>
                    <div className="flex flex-col">
                      {items.map((f) => {
                        const active = f.id === feature.id;
                        const capOn = f.capability ? app?.channel_capabilities[f.capability] ?? true : f.requiresEdit ? app?.message_edit_enabled ?? true : true;
                        return (
                          <button
                            key={f.id}
                            onClick={() => setFeatureId(f.id)}
                            className={cx(
                              "flex items-center gap-2 rounded-lg px-2 py-1.5 text-left text-sm transition-colors duration-150",
                              active ? "bg-accent-soft font-medium text-accent" : "text-text-muted hover:bg-surface-2 hover:text-text"
                            )}
                          >
                            <span
                              className={cx("h-1.5 w-1.5 shrink-0 rounded-full", capOn ? "bg-success" : "bg-warning")}
                              title={capOn ? "Enabled for this app" : "Switched off for this app"}
                            />
                            <span className="truncate">{f.label}</span>
                          </button>
                        );
                      })}
                    </div>
                  </div>
                );
              })}
            </nav>

            <FeaturePanel
              feature={feature}
              app={app}
              values={values}
              onChange={(next) => setValuesByFeature((prev) => ({ ...prev, [feature.id]: next }))}
              onRun={run}
              running={playground.running}
              lastRecord={lastRecord}
              ctx={ctx}
              userOptions={userOptions}
              recent={playground.recent}
              hasActor={!!playground.activeActor}
            />

            <div className="xl:sticky xl:top-0">
              <LivePanel
                actor={playground.activeActor}
                channelId={playground.channelId}
                events={playground.events}
                requests={playground.requests}
                recent={playground.recent}
                nameOf={nameOf}
                onSelectMessage={(id) => playground.setRecentId("message", id)}
                onClear={playground.clearLogs}
              />
            </div>
          </div>
        </>
      )}

      {apps && apps.length > 0 && !playground.activeActor && (
        <p className="flex items-center gap-2 text-sm text-text-faint">
          <FlaskConical className="h-4 w-4" />
          Tip: add two actors and keep both realtime connections on — then send as one and watch the other&apos;s events.
        </p>
      )}
    </div>
  );
}
