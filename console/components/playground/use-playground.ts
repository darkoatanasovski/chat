"use client";

// All of the Playground's per-app state in one hook: the actors (end-users
// with minted tokens) it can act as, one realtime socket per actor, the
// request and event logs, and the "recent ids" that pre-fill follow-up
// forms. The page and its panels are pure renderers over this.
//
// Actors (tokens included) live in sessionStorage, keyed per app — they're
// short-lived playground tokens, so per-tab and gone on close is exactly
// the right lifetime. Everything durable but harmless (which channel was
// selected, which actor was acting) goes to localStorage instead.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, mintEndUserToken } from "@/lib/api";
import type { EndUser } from "@/lib/types";
import {
  API_BASE,
  GATEWAY_BASE,
  runHttp,
  tokenNeedsRefresh,
  type Actor,
  type RequestRecord,
  type RunResult,
} from "@/lib/playground/client";
import { PlaygroundSocket, type RealtimeFrame, type SocketStatus } from "@/lib/playground/realtime";
import { captureRecent, type Feature, type FeatureContext, type RecentIds, type RecentKind, type Values } from "@/lib/playground/features";

export interface EventRecord {
  id: number;
  at: number;
  /** Which actor's socket received this frame. */
  actor: Pick<Actor, "userId" | "displayName">;
  frame: RealtimeFrame;
}

interface Prefs {
  channelId?: string | null;
  activeActorId?: string | null;
  /** userId -> realtime enabled; absent means enabled. */
  realtime?: Record<string, boolean>;
}

const MAX_LOG = 250;

const actorsKey = (appId: number) => `chat-console:playground:${appId}:actors`;
const prefsKey = (appId: number) => `chat-console:playground:${appId}:prefs`;

function readJSON<T>(storage: Storage | undefined, key: string, fallback: T): T {
  try {
    const raw = storage?.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function writeJSON(storage: Storage | undefined, key: string, value: unknown) {
  try {
    storage?.setItem(key, JSON.stringify(value));
  } catch {
    // Storage disabled — the Playground still works, it just won't remember.
  }
}

let nextId = 1;

export function usePlayground(orgToken: string, appId: number | null) {
  const [actors, setActors] = useState<Actor[]>([]);
  const [activeActorId, setActiveActorIdState] = useState<string | null>(null);
  const [channelId, setChannelIdState] = useState<string | null>(null);
  const [realtime, setRealtime] = useState<Record<string, boolean>>({});
  const [socketStatus, setSocketStatus] = useState<Record<string, SocketStatus>>({});
  const [events, setEvents] = useState<EventRecord[]>([]);
  const [requests, setRequests] = useState<RequestRecord[]>([]);
  const [recent, setRecent] = useState<RecentIds>({});
  const [running, setRunning] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const sockets = useRef(new Map<string, { socket: PlaygroundSocket; token: string }>());

  // ---- (re)load per-app state ----
  useEffect(() => {
    setLoaded(false);
    setEvents([]);
    setRequests([]);
    setRecent({});
    if (appId === null) {
      setActors([]);
      setActiveActorIdState(null);
      setChannelIdState(null);
      setRealtime({});
      setLoaded(true);
      return;
    }
    const storedActors = readJSON<Actor[]>(typeof window !== "undefined" ? window.sessionStorage : undefined, actorsKey(appId), []);
    const prefs = readJSON<Prefs>(typeof window !== "undefined" ? window.localStorage : undefined, prefsKey(appId), {});
    setActors(storedActors);
    setActiveActorIdState(
      prefs.activeActorId && storedActors.some((a) => a.userId === prefs.activeActorId) ? prefs.activeActorId : storedActors[0]?.userId ?? null
    );
    setChannelIdState(prefs.channelId ?? null);
    setRealtime(prefs.realtime ?? {});
    setLoaded(true);
  }, [appId]);

  // ---- persist ----
  useEffect(() => {
    if (!loaded || appId === null) return;
    writeJSON(window.sessionStorage, actorsKey(appId), actors);
  }, [actors, appId, loaded]);

  useEffect(() => {
    if (!loaded || appId === null) return;
    writeJSON(window.localStorage, prefsKey(appId), { channelId, activeActorId, realtime } satisfies Prefs);
  }, [channelId, activeActorId, realtime, appId, loaded]);

  // ---- sockets: one per actor with realtime on ----
  useEffect(() => {
    if (!loaded) return;
    const wanted = new Map(actors.filter((a) => realtime[a.userId] !== false).map((a) => [a.userId, a]));

    for (const [userId, entry] of sockets.current) {
      const actor = wanted.get(userId);
      // Also tear down a socket whose token was re-minted so the next
      // reconnect doesn't retry with an expired one.
      if (!actor || actor.token !== entry.token) {
        entry.socket.close();
        sockets.current.delete(userId);
        setSocketStatus((s) => {
          const next = { ...s };
          delete next[userId];
          return next;
        });
      }
    }

    for (const actor of wanted.values()) {
      if (sockets.current.has(actor.userId)) continue;
      const who = { userId: actor.userId, displayName: actor.displayName };
      const socket = new PlaygroundSocket({
        wsBase: GATEWAY_BASE,
        token: actor.token,
        onStatus: (status) => setSocketStatus((s) => ({ ...s, [actor.userId]: status })),
        onFrame: (frame) =>
          setEvents((prev) => [{ id: nextId++, at: Date.now(), actor: who, frame }, ...prev].slice(0, MAX_LOG)),
      });
      sockets.current.set(actor.userId, { socket, token: actor.token });
    }
  }, [actors, realtime, loaded]);

  useEffect(() => {
    const current = sockets.current;
    return () => {
      for (const entry of current.values()) entry.socket.close();
      current.clear();
    };
  }, []);

  // ---- actors ----
  const mint = useCallback(
    async (user: Pick<EndUser, "user_id" | "display_name" | "region">): Promise<Actor> => {
      if (appId === null) throw new Error("no app selected");
      const minted = await mintEndUserToken(orgToken, appId, user.user_id);
      return {
        userId: user.user_id,
        displayName: user.display_name,
        region: user.region,
        token: minted.token,
        expiresAt: minted.expires_at,
      };
    },
    [orgToken, appId]
  );

  const addActor = useCallback(
    async (user: Pick<EndUser, "user_id" | "display_name" | "region">) => {
      const actor = await mint(user);
      setActors((prev) => {
        const without = prev.filter((a) => a.userId !== actor.userId);
        return [...without, actor];
      });
      setActiveActorIdState((current) => current ?? actor.userId);
      return actor;
    },
    [mint]
  );

  const removeActor = useCallback((userId: string) => {
    setActors((prev) => prev.filter((a) => a.userId !== userId));
    setActiveActorIdState((current) => (current === userId ? null : current));
  }, []);

  /** Re-mints an actor's token if it's expired or about to — keeps a
   * long-running Playground tab working past the token's TTL without
   * anyone having to notice. */
  const ensureFresh = useCallback(
    async (actor: Actor): Promise<Actor> => {
      if (!tokenNeedsRefresh(actor)) return actor;
      const fresh = await mint({ user_id: actor.userId, display_name: actor.displayName, region: actor.region });
      setActors((prev) => prev.map((a) => (a.userId === fresh.userId ? fresh : a)));
      return fresh;
    },
    [mint]
  );

  const activeActor = useMemo(() => actors.find((a) => a.userId === activeActorId) ?? null, [actors, activeActorId]);

  // When the active actor is removed (or none was restored), fall back to
  // whichever actor is first so the Playground is never "acting as nobody"
  // while actors exist.
  useEffect(() => {
    if (!loaded) return;
    if (!activeActor && actors.length > 0) setActiveActorIdState(actors[0].userId);
  }, [activeActor, actors, loaded]);

  const toggleRealtime = useCallback((userId: string) => {
    setRealtime((prev) => ({ ...prev, [userId]: prev[userId] === false }));
  }, []);

  // ---- running features ----
  const setRecentId = useCallback((kind: RecentKind, id: string) => {
    setRecent((prev) => ({ ...prev, [kind]: id }));
  }, []);

  const context = useCallback(
    (actor: Actor): FeatureContext => ({
      apiBase: API_BASE,
      wsBase: GATEWAY_BASE,
      channelId: channelId ?? "",
      actor: { userId: actor.userId, displayName: actor.displayName },
      recent,
    }),
    [channelId, recent]
  );

  const run = useCallback(
    async (feature: Feature, values: Values): Promise<RequestRecord | null> => {
      if (!activeActor || !feature.build) return null;
      setRunning(true);
      try {
        const actor = await ensureFresh(activeActor);
        const spec = feature.build(values, context(actor));
        const who = { userId: actor.userId, displayName: actor.displayName };
        let result: RunResult | undefined;
        if (spec.kind === "ws") {
          const sent = sockets.current.get(actor.userId)?.socket.send(spec.frame) ?? false;
          result = {
            status: sent ? 0 : -1,
            ok: sent,
            response: sent ? { sent: true, frame: spec.frame } : { error: "this actor's realtime connection isn't open — enable it in the setup bar" },
            durationMs: 0,
          };
        } else {
          result = await runHttp(API_BASE, actor.token, spec);
          if (result.ok) setRecent((prev) => ({ ...prev, ...captureRecent(result?.response) }));
        }
        const record: RequestRecord = { id: nextId++, at: Date.now(), actor: who, spec, result, featureId: feature.id };
        setRequests((prev) => [record, ...prev].slice(0, MAX_LOG));
        return record;
      } catch (err) {
        const message = err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err);
        const record: RequestRecord = {
          id: nextId++,
          at: Date.now(),
          actor: { userId: activeActor.userId, displayName: activeActor.displayName },
          spec: { kind: "http", method: "GET", path: "(network error)" },
          result: { status: -1, ok: false, response: { error: message }, durationMs: 0 },
          featureId: feature.id,
        };
        setRequests((prev) => [record, ...prev].slice(0, MAX_LOG));
        return record;
      } finally {
        setRunning(false);
      }
    },
    [activeActor, ensureFresh, context]
  );

  const clearLogs = useCallback(() => {
    setEvents([]);
    setRequests([]);
  }, []);

  /** Display name for any user id the Playground has seen as an actor —
   * panels fall back to a shortened id otherwise. */
  const actorName = useCallback(
    (userId: string) => actors.find((a) => a.userId === userId)?.displayName,
    [actors]
  );

  return {
    loaded,
    actors,
    activeActor,
    setActiveActorId: setActiveActorIdState,
    addActor,
    removeActor,
    channelId,
    setChannelId: setChannelIdState,
    realtime,
    toggleRealtime,
    socketStatus,
    events,
    requests,
    recent,
    setRecentId,
    running,
    run,
    clearLogs,
    actorName,
    context,
  };
}

export type Playground = ReturnType<typeof usePlayground>;
