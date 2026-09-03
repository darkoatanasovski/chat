"use client";

// The Playground's top strip: which app, which channel, and which end-users
// ("actors") the Playground can act as — plus each actor's realtime
// connection and channel membership at a glance.
import { useState, type FormEvent } from "react";
import { Hash, Plus, Radio, UserPlus, X, Zap } from "lucide-react";
import { ApiError } from "@/lib/api";
import {
  useAddChannelMemberMutation,
  useChannelMembersQuery,
  useCreateDashboardChannelMutation,
  useCreateEndUserMutation,
  useDashboardChannelsQuery,
  useEndUsersQuery,
} from "@/lib/queries";
import type { AppSummary, EndUser } from "@/lib/types";
import { useSession } from "@/components/shell";
import { useToast } from "@/components/toast";
import { Avatar, Badge, Button, ErrorBanner, Input, Label, Modal, Panel, Select, Skeleton, cx } from "@/components/ui";
import type { Playground } from "./use-playground";

const REGIONS = [
  { value: "eu", label: "Europe" },
  { value: "us", label: "North America" },
  { value: "asia", label: "Asia Pacific" },
];

export function SetupBar({
  apps,
  appId,
  onAppChange,
  playground,
}: {
  apps: AppSummary[];
  appId: number | null;
  onAppChange: (appId: number) => void;
  playground: Playground;
}) {
  const { session } = useSession();
  const toast = useToast();
  const [addOpen, setAddOpen] = useState(false);
  const [newChannelOpen, setNewChannelOpen] = useState(false);

  // Disabled until an app is resolved — otherwise the first render fires a
  // doomed GET /dashboard/apps/0/channels.
  const channelsQuery = useDashboardChannelsQuery(session.token, appId ?? 0, appId !== null);
  const channels = appId !== null ? channelsQuery.data ?? [] : [];
  const membersQuery = useChannelMembersQuery(session.token, playground.channelId ?? undefined, { enabled: !!playground.channelId });
  const memberIds = new Set((membersQuery.data ?? []).map((m) => m.user_id));
  const addMember = useAddChannelMemberMutation(session.token, appId ?? 0);

  async function join(userId: string) {
    if (!playground.channelId) return;
    try {
      await addMember.mutateAsync({ channelId: playground.channelId, userId });
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    }
  }

  return (
    <Panel animate={false} className="p-5">
      <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div>
          <Label>App</Label>
          <Select value={appId ?? ""} onChange={(e) => onAppChange(Number(e.target.value))}>
            {apps.map((a) => (
              <option key={a.app_id} value={a.app_id}>
                {a.name} · #{a.app_id}
              </option>
            ))}
          </Select>
        </div>
        <div>
          <Label>Channel</Label>
          <div className="flex items-center gap-2">
            <Select
              value={playground.channelId ?? ""}
              onChange={(e) => playground.setChannelId(e.target.value || null)}
              disabled={appId === null}
            >
              <option value="">— none selected —</option>
              {channels.map((c) => (
                <option key={c.channel_id} value={c.channel_id}>
                  #{c.name} · {c.member_count} member{c.member_count === 1 ? "" : "s"} · {c.home_region}
                </option>
              ))}
            </Select>
            <Button
              variant="secondary"
              icon={<Plus className="h-4 w-4" />}
              onClick={() => setNewChannelOpen(true)}
              disabled={appId === null || playground.actors.length === 0}
              title={playground.actors.length === 0 ? "Add an actor first — a channel needs a creator" : "Create a channel"}
              className="shrink-0"
            >
              New
            </Button>
          </div>
        </div>
      </div>

      <div className="mt-5 border-t border-border-soft pt-4">
        <div className="mb-2.5 flex items-center justify-between">
          <div className="text-sm font-medium text-text-muted">
            Actors{" "}
            <span className="font-normal text-text-faint">— end-users you act as. Click one to act as it; add two to see realtime between them.</span>
          </div>
          <Button variant="secondary" icon={<UserPlus className="h-4 w-4" />} onClick={() => setAddOpen(true)} disabled={appId === null} className="px-3 py-1.5 text-sm">
            Add actor
          </Button>
        </div>

        {playground.actors.length === 0 ? (
          <div className="rounded-xl border border-dashed border-border px-4 py-5 text-center text-sm text-text-faint">
            No actors yet. Add one of this app&apos;s end-users (or create a new one) to start.
          </div>
        ) : (
          <div className="flex flex-wrap gap-2">
            {playground.actors.map((actor) => {
              const active = playground.activeActor?.userId === actor.userId;
              const status = playground.socketStatus[actor.userId];
              const rtOn = playground.realtime[actor.userId] !== false;
              const inChannel = playground.channelId ? memberIds.has(actor.userId) : null;
              return (
                <div
                  key={actor.userId}
                  className={cx(
                    "flex items-center gap-2.5 rounded-xl border py-1.5 pl-2 pr-1.5 transition-colors duration-150",
                    active ? "border-accent/50 bg-accent-soft" : "border-border bg-surface-2 hover:border-text-faint"
                  )}
                >
                  <button onClick={() => playground.setActiveActorId(actor.userId)} className="flex items-center gap-2.5 text-left" title={actor.userId}>
                    <span className="relative">
                      <Avatar name={actor.displayName} size="sm" />
                      <span
                        className={cx(
                          "absolute -bottom-0.5 -right-0.5 block h-2.5 w-2.5 rounded-full ring-2 ring-surface-2",
                          !rtOn && "bg-text-faint/40",
                          rtOn && status === "open" && "bg-success",
                          rtOn && status === "connecting" && "bg-warning",
                          rtOn && (status === "closed" || !status) && "bg-danger"
                        )}
                        title={!rtOn ? "Realtime off" : status === "open" ? "Realtime connected" : status === "connecting" ? "Connecting…" : "Disconnected"}
                      />
                    </span>
                    <span className="min-w-0">
                      <span className="block truncate text-sm font-medium text-text">
                        {actor.displayName}
                        {active && <span className="ml-1.5 font-mono text-[10px] uppercase text-accent">acting</span>}
                      </span>
                      <span className="block font-mono text-[11px] text-text-faint">
                        {actor.region}
                        {inChannel === false && (
                          <>
                            {" · "}
                            <span className="text-warning">not in channel</span>
                          </>
                        )}
                      </span>
                    </span>
                  </button>
                  {inChannel === false && (
                    <button
                      onClick={() => join(actor.userId)}
                      disabled={addMember.isPending}
                      className="rounded-lg border border-warning/40 px-2 py-1 text-[11px] font-medium text-warning transition-colors duration-150 hover:bg-warning-soft"
                    >
                      Join
                    </button>
                  )}
                  <button
                    onClick={() => playground.toggleRealtime(actor.userId)}
                    className={cx("rounded-lg p-1.5 transition-colors duration-150", rtOn ? "text-success hover:bg-surface" : "text-text-faint hover:text-text")}
                    title={rtOn ? "Disconnect realtime" : "Connect realtime"}
                  >
                    <Radio className="h-3.5 w-3.5" />
                  </button>
                  <button
                    onClick={() => playground.removeActor(actor.userId)}
                    className="rounded-lg p-1.5 text-text-faint transition-colors duration-150 hover:text-danger"
                    title="Remove actor (the end-user itself is kept)"
                  >
                    <X className="h-3.5 w-3.5" />
                  </button>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {appId !== null && (
        <AddActorModal open={addOpen} onClose={() => setAddOpen(false)} appId={appId} playground={playground} />
      )}
      {appId !== null && (
        <NewChannelModal
          open={newChannelOpen}
          onClose={() => setNewChannelOpen(false)}
          appId={appId}
          playground={playground}
        />
      )}
    </Panel>
  );
}

function AddActorModal({ open, onClose, appId, playground }: { open: boolean; onClose: () => void; appId: number; playground: Playground }) {
  const { session } = useSession();
  const toast = useToast();
  const usersQuery = useEndUsersQuery(session.token, appId);
  const createUser = useCreateEndUserMutation(session.token, appId);
  const [name, setName] = useState("");
  const [region, setRegion] = useState("eu");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const actorIds = new Set(playground.actors.map((a) => a.userId));
  const candidates = (usersQuery.data ?? []).filter((u) => !actorIds.has(u.user_id));

  async function add(user: EndUser) {
    setError(null);
    setBusy(user.user_id);
    try {
      await playground.addActor(user);
      toast("success", `Acting as ${user.display_name} is now possible`);
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  async function create(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy("new");
    try {
      const user = await createUser.mutateAsync({ displayName: name.trim(), region });
      await playground.addActor(user);
      setName("");
      toast("success", `Created ${user.display_name}`);
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="Add an actor" icon={<UserPlus className="h-4 w-4 text-accent" />} widthClass="max-w-lg">
      <div className="flex flex-col gap-5">
        <div>
          <div className="mb-2 text-sm font-medium text-text-muted">Existing end-users</div>
          {usersQuery.isLoading && <Skeleton className="h-10" />}
          {!usersQuery.isLoading && candidates.length === 0 && (
            <p className="text-sm text-text-faint">
              {(usersQuery.data?.length ?? 0) === 0
                ? "This app has no end-users yet — create one below."
                : "Every end-user of this app is already an actor — create a new one below."}
            </p>
          )}
          <div className="flex max-h-56 flex-col gap-1 overflow-y-auto">
            {candidates.map((u) => (
              <button
                key={u.user_id}
                onClick={() => add(u)}
                disabled={busy !== null}
                className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 text-left transition-colors duration-150 hover:bg-surface-2 disabled:opacity-50"
              >
                <Avatar name={u.display_name} size="sm" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm text-text">{u.display_name}</span>
                  <span className="block font-mono text-[11px] text-text-faint">{u.region} · {u.user_id}</span>
                </span>
                <Badge tone={busy === u.user_id ? "accent" : "default"}>{busy === u.user_id ? "minting…" : "use"}</Badge>
              </button>
            ))}
          </div>
        </div>

        <form onSubmit={create} className="flex flex-col gap-3 border-t border-border-soft pt-5">
          <div className="text-sm font-medium text-text-muted">Or create a new end-user</div>
          <div className="grid grid-cols-[minmax(0,1fr)_150px] gap-3">
            <div>
              <Label>Display name</Label>
              <Input required value={name} onChange={(e) => setName(e.target.value)} placeholder="Ada" />
            </div>
            <div>
              <Label>Region</Label>
              <Select value={region} onChange={(e) => setRegion(e.target.value)}>
                {REGIONS.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </Select>
            </div>
          </div>
          <Button type="submit" variant="primary" loading={busy === "new"} className="justify-center">
            Create and add
          </Button>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </form>
      </div>
    </Modal>
  );
}

function NewChannelModal({ open, onClose, appId, playground }: { open: boolean; onClose: () => void; appId: number; playground: Playground }) {
  const { session } = useSession();
  const toast = useToast();
  const createChannel = useCreateDashboardChannelMutation(session.token, appId);
  const addMember = useAddChannelMemberMutation(session.token, appId);
  const [name, setName] = useState("");
  const [addEveryone, setAddEveryone] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const creator = playground.activeActor ?? playground.actors[0] ?? null;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!creator) return;
    setError(null);
    try {
      const channel = await createChannel.mutateAsync({ name: name.trim(), creatorUserId: creator.userId });
      if (addEveryone) {
        for (const actor of playground.actors) {
          if (actor.userId === creator.userId) continue;
          await addMember.mutateAsync({ channelId: channel.channel_id, userId: actor.userId });
        }
      }
      playground.setChannelId(channel.channel_id);
      setName("");
      toast("success", `#${channel.name} created`);
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="New channel" icon={<Hash className="h-4 w-4 text-accent" />}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <div>
          <Label>Name</Label>
          <Input required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="playground" />
        </div>
        <p className="text-sm text-text-faint">
          Created by <span className="text-text">{creator?.displayName ?? "—"}</span> (the acting actor).
        </p>
        <label className="flex items-center gap-2.5 text-sm text-text-muted">
          <input type="checkbox" checked={addEveryone} onChange={(e) => setAddEveryone(e.target.checked)} className="accent-accent" />
          Add every other actor as a member
        </label>
        <Button type="submit" variant="primary" loading={createChannel.isPending || addMember.isPending} icon={<Zap className="h-4 w-4" />} className="justify-center">
          Create channel
        </Button>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  );
}
