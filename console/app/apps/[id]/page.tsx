"use client";

import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useParams } from "next/navigation";
import { motion } from "framer-motion";
import { ArrowLeft, ArrowRight, Check, Copy, Hash, KeyRound, MessagesSquare, Plus, ShieldOff, Trash2, Users as UsersIcon, X } from "lucide-react";
import {
  addChannelMember,
  createCredential,
  createDashboardChannel,
  createEndUser,
  listApps,
  listChannelMembers,
  listCredentials,
  listDashboardBlocks,
  listDashboardChannels,
  listEndUsers,
  removeChannelMember,
  revokeCredential,
  ApiError,
} from "@/lib/api";
import type { AppSummary, ChannelMember, Credential, DashboardBlock, DashboardChannel, EndUser } from "@/lib/types";
import { ConsoleShell, useSession } from "@/components/shell";
import { useToast } from "@/components/toast";
import { Avatar, Badge, Button, ErrorBanner, Input, Label, Modal, Panel, Select, Skeleton } from "@/components/ui";

export default function AppDetailPage() {
  return (
    <ConsoleShell>
      <AppDetailView />
    </ConsoleShell>
  );
}

const TABS = [
  { id: "credentials", label: "Credentials" },
  { id: "users", label: "End-users" },
  { id: "channels", label: "Channels" },
  { id: "blocks", label: "Blocks" },
] as const;

type TabID = (typeof TABS)[number]["id"];

function AppDetailView() {
  const { session } = useSession();
  const params = useParams<{ id: string }>();
  const appId = Number(params.id);

  const [app, setApp] = useState<AppSummary | null>(null);
  const [tab, setTab] = useState<TabID>("credentials");

  useEffect(() => {
    listApps(session.token, session.org.org_id)
      .then((apps) => setApp(apps.find((a) => a.app_id === appId) ?? null))
      .catch(() => {});
  }, [session.token, session.org.org_id, appId]);

  return (
    <div>
      <a href="/apps" className="mb-6 inline-flex items-center gap-1.5 text-[15px] text-text-muted transition-colors duration-150 hover:text-text">
        <ArrowLeft className="h-4 w-4" />
        Apps
      </a>

      <div className="mb-8 flex items-center gap-3.5">
        <span className="grid h-12 w-12 place-items-center rounded-full bg-accent-soft text-accent">
          <KeyRound className="h-5.5 w-5.5" />
        </span>
        <div>
          <h1 className="text-2xl font-semibold text-text">{app?.name ?? <Skeleton className="h-7 w-40" />}</h1>
          <p className="mt-0.5 font-mono text-xs text-text-faint">app_id: {appId}</p>
        </div>
      </div>

      <div className="mb-7 flex items-center gap-1 border-b border-border-soft">
        {TABS.map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={`relative px-4 py-3 text-[15px] transition-colors duration-150 ${
              tab === t.id ? "text-accent font-medium" : "text-text-muted hover:text-text"
            }`}
          >
            {t.label}
            {tab === t.id && <motion.span layoutId="app-tab-active" className="absolute inset-x-0 -bottom-px h-0.5 rounded-full bg-accent" />}
          </button>
        ))}
      </div>

      {tab === "credentials" && <CredentialsTab appId={appId} />}
      {tab === "users" && <EndUsersTab appId={appId} />}
      {tab === "channels" && <ChannelsTab appId={appId} />}
      {tab === "blocks" && <BlocksTab appId={appId} />}
    </div>
  );
}

function regionLabel(region: string) {
  return { eu: "Europe", us: "North America", asia: "Asia Pacific" }[region] ?? region;
}

// ---- Credentials ----

function CredentialsTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const toast = useToast();
  const [credentials, setCredentials] = useState<Credential[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [newCredential, setNewCredential] = useState<Credential | null>(null);
  const [revoking, setRevoking] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  function refresh() {
    listCredentials(session.token, appId)
      .then(setCredentials)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }

  useEffect(refresh, [session.token, appId]);

  async function handleCreateCredential() {
    setError(null);
    try {
      const cred = await createCredential(session.token, appId);
      setNewCredential(cred);
      refresh();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  async function handleRevoke(credentialId: string) {
    setRevoking(credentialId);
    try {
      await revokeCredential(session.token, appId, credentialId);
      toast("success", "Credential revoked");
      refresh();
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    } finally {
      setRevoking(null);
    }
  }

  function copy(text: string, id: string) {
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setCopied(id);
        setTimeout(() => setCopied(null), 1500);
      })
      .catch(() => toast("error", "Couldn't copy automatically — select and copy the text manually."));
  }

  return (
    <div>
      <div className="mb-5 flex justify-end">
        <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={handleCreateCredential}>
          Generate credential
        </Button>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {credentials === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {credentials?.length === 0 && <EmptyState text="No credentials yet — generate one above." />}
        <div className="flex flex-col gap-2">
          {credentials?.map((c, i) => {
            const active = !c.revoked_at;
            return (
              <motion.div
                key={c.credential_id}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.04 }}
                className="flex items-center gap-4 rounded-xl border border-border-soft px-4 py-3.5"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <code className="truncate font-mono text-[13px] text-text">{c.key}</code>
                    <button
                      onClick={() => copy(c.key, c.credential_id)}
                      className="shrink-0 text-text-faint transition-colors duration-150 hover:text-text"
                      title="Copy key"
                    >
                      {copied === c.credential_id ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
                    </button>
                  </div>
                  <div className="mt-1 text-xs text-text-faint">
                    Created {new Date(c.created_at).toLocaleDateString()}
                    {c.revoked_at && ` · Revoked ${new Date(c.revoked_at).toLocaleDateString()}`}
                  </div>
                </div>
                <Badge tone={active ? "success" : "default"}>{active ? "active" : "revoked"}</Badge>
                {active && (
                  <Button
                    variant="ghost"
                    className="px-2.5 text-danger hover:bg-danger-soft"
                    loading={revoking === c.credential_id}
                    onClick={() => handleRevoke(c.credential_id)}
                    icon={<Trash2 className="h-4 w-4" />}
                  />
                )}
              </motion.div>
            );
          })}
        </div>
      </Panel>

      <Modal
        open={newCredential !== null}
        onClose={() => setNewCredential(null)}
        title="New credential generated"
        icon={<KeyRound className="h-4 w-4 text-accent" />}
        widthClass="max-w-lg"
      >
        {newCredential && (
          <div className="flex flex-col gap-4">
            <ErrorBanner>
              <span className="text-text">
                This secret is shown <strong>once</strong> — copy it now.
              </span>
            </ErrorBanner>
            <div>
              <Label>API key</Label>
              <div className="flex items-center gap-2">
                <code className="flex-1 truncate rounded-xl border border-border bg-surface-2 px-3.5 py-2.5 font-mono text-[13px] text-text">
                  {newCredential.key}
                </code>
                <Button
                  variant="secondary"
                  onClick={() => copy(newCredential.key, "new-key")}
                  icon={copied === "new-key" ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
                />
              </div>
            </div>
            <div>
              <Label>API secret</Label>
              <div className="flex items-center gap-2">
                <code className="flex-1 truncate rounded-xl border border-border bg-surface-2 px-3.5 py-2.5 font-mono text-[13px] text-text">
                  {newCredential.secret}
                </code>
                <Button
                  variant="secondary"
                  onClick={() => copy(newCredential.secret ?? "", "new-secret")}
                  icon={copied === "new-secret" ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
                />
              </div>
            </div>
            <Button variant="primary" className="justify-center" onClick={() => setNewCredential(null)}>
              Done
            </Button>
          </div>
        )}
      </Modal>
    </div>
  );
}

// ---- End-users ----

function EndUsersTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const toast = useToast();
  const [users, setUsers] = useState<EndUser[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  function refresh() {
    listEndUsers(session.token, appId)
      .then(setUsers)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }

  useEffect(refresh, [session.token, appId]);

  return (
    <div>
      <div className="mb-5 flex justify-end">
        <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setCreateOpen(true)}>
          Add end-user
        </Button>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {users === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {users?.length === 0 && <EmptyState text="No end-users yet — add one to start testing channels." />}
        <div className="flex flex-col gap-2">
          {users?.map((u, i) => (
            <motion.div
              key={u.user_id}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.03 }}
              className="flex items-center gap-3.5 rounded-xl border border-border-soft px-4 py-3.5"
            >
              <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-surface-2 text-sm font-semibold text-text-muted">
                {u.display_name.slice(0, 1).toUpperCase()}
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[15px] text-text">{u.display_name}</div>
                <div className="mt-0.5 font-mono text-xs text-text-faint">{u.user_id}</div>
              </div>
              <Badge>{u.region.toUpperCase()}</Badge>
              <span className="hidden text-sm text-text-faint sm:inline">{new Date(u.created_at).toLocaleDateString()}</span>
            </motion.div>
          ))}
        </div>
      </Panel>

      <CreateEndUserModal
        appId={appId}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          refresh();
          toast("success", "End-user added");
        }}
      />
    </div>
  );
}

function CreateEndUserModal({
  appId,
  open,
  onClose,
  onCreated,
}: {
  appId: number;
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { session } = useSession();
  const [displayName, setDisplayName] = useState("");
  const [region, setRegion] = useState("eu");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setDisplayName("");
    setRegion("eu");
    setError(null);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await createEndUser(session.token, appId, displayName.trim(), region);
      onCreated();
      onClose();
      setTimeout(reset, 200);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        onClose();
        setTimeout(reset, 200);
      }}
      title="Add an end-user"
      icon={<UsersIcon className="h-4 w-4 text-accent" />}
      widthClass="max-w-md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div>
          <Label>Display name</Label>
          <Input required autoFocus value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="Jane Doe" />
        </div>
        <div>
          <Label>Region</Label>
          <Select value={region} onChange={(e) => setRegion(e.target.value)}>
            <option value="eu">Europe</option>
            <option value="us">North America</option>
            <option value="asia">Asia Pacific</option>
          </Select>
        </div>
        <Button type="submit" variant="primary" loading={loading} className="justify-center">
          Add end-user
        </Button>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  );
}

// ---- Channels ----

function ChannelsTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const toast = useToast();
  const [channels, setChannels] = useState<DashboardChannel[] | null>(null);
  const [users, setUsers] = useState<EndUser[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [activeChannel, setActiveChannel] = useState<DashboardChannel | null>(null);

  function refresh() {
    listDashboardChannels(session.token, appId)
      .then(setChannels)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
    listEndUsers(session.token, appId)
      .then(setUsers)
      .catch(() => {});
  }

  useEffect(refresh, [session.token, appId]);

  return (
    <div>
      <div className="mb-5 flex justify-end">
        <Button
          variant="primary"
          icon={<Plus className="h-4 w-4" />}
          disabled={users !== null && users.length === 0}
          onClick={() => setCreateOpen(true)}
        >
          Create channel
        </Button>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      {users?.length === 0 && (
        <div className="mb-5">
          <ErrorBanner>
            <span className="text-text">Add at least one end-user first — every channel needs an end-user as its creator.</span>
          </ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {channels === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {channels?.length === 0 && <EmptyState text="No channels yet — create one to start testing message delivery." />}
        <div className="flex flex-col gap-2">
          {channels?.map((c, i) => (
            <motion.button
              key={c.channel_id}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.03 }}
              onClick={() => setActiveChannel(c)}
              className="flex items-center gap-3.5 rounded-xl border border-border-soft px-4 py-3.5 text-left transition-colors duration-150 hover:border-accent/30"
            >
              <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-accent-soft text-accent">
                <Hash className="h-4 w-4" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[15px] text-text">{c.name}</div>
                <div className="mt-0.5 text-xs text-text-faint">
                  Created by {c.creator_name} · {regionLabel(c.home_region)}
                </div>
              </div>
              <div className="flex items-center gap-1.5 text-text-muted">
                <UsersIcon className="h-4 w-4" />
                <span className="font-mono text-[15px] text-text">{c.member_count}</span>
              </div>
            </motion.button>
          ))}
        </div>
      </Panel>

      <CreateChannelModal
        appId={appId}
        users={users ?? []}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          refresh();
          toast("success", "Channel created");
        }}
      />

      <ManageMembersModal
        channel={activeChannel}
        appUsers={users ?? []}
        onClose={() => setActiveChannel(null)}
        onChanged={refresh}
      />
    </div>
  );
}

function CreateChannelModal({
  appId,
  users,
  open,
  onClose,
  onCreated,
}: {
  appId: number;
  users: EndUser[];
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { session } = useSession();
  const [name, setName] = useState("");
  const [creatorId, setCreatorId] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setName("");
    setCreatorId("");
    setError(null);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await createDashboardChannel(session.token, appId, name.trim(), creatorId || users[0]?.user_id || "");
      onCreated();
      onClose();
      setTimeout(reset, 200);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        onClose();
        setTimeout(reset, 200);
      }}
      title="Create a channel"
      icon={<Hash className="h-4 w-4 text-accent" />}
      widthClass="max-w-md"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div>
          <Label>Channel name</Label>
          <Input required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="general" />
        </div>
        <div>
          <Label>Creator</Label>
          <Select value={creatorId} onChange={(e) => setCreatorId(e.target.value)}>
            {users.map((u) => (
              <option key={u.user_id} value={u.user_id}>
                {u.display_name}
              </option>
            ))}
          </Select>
        </div>
        <Button type="submit" variant="primary" loading={loading} className="justify-center">
          Create channel
        </Button>
        {error && <ErrorBanner>{error}</ErrorBanner>}
      </form>
    </Modal>
  );
}

function ManageMembersModal({
  channel,
  appUsers,
  onClose,
  onChanged,
}: {
  channel: DashboardChannel | null;
  appUsers: EndUser[];
  onClose: () => void;
  onChanged: () => void;
}) {
  const { session } = useSession();
  const toast = useToast();
  const [members, setMembers] = useState<ChannelMember[] | null>(null);
  const [addUserId, setAddUserId] = useState("");
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  function refresh() {
    if (!channel) return;
    listChannelMembers(session.token, channel.channel_id)
      .then(setMembers)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }

  useEffect(() => {
    setMembers(null);
    setError(null);
    setAddUserId("");
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channel?.channel_id]);

  if (!channel) return null;

  const memberIds = new Set(members?.map((m) => m.user_id));
  const candidates = appUsers.filter((u) => !memberIds.has(u.user_id));

  async function handleAdd() {
    if (!channel || !addUserId) return;
    setAdding(true);
    setError(null);
    try {
      await addChannelMember(session.token, channel.channel_id, addUserId);
      setAddUserId("");
      refresh();
      onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setAdding(false);
    }
  }

  async function handleRemove(userId: string) {
    if (!channel) return;
    setRemoving(userId);
    try {
      await removeChannelMember(session.token, channel.channel_id, userId);
      refresh();
      onChanged();
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    } finally {
      setRemoving(null);
    }
  }

  return (
    <Modal open={channel !== null} onClose={onClose} title={`#${channel.name}`} icon={<Hash className="h-4 w-4 text-accent" />} widthClass="max-w-lg">
      <div className="flex flex-col gap-4">
        {error && <ErrorBanner>{error}</ErrorBanner>}

        {candidates.length > 0 && (
          <div className="flex items-center gap-2">
            <Select value={addUserId} onChange={(e) => setAddUserId(e.target.value)} className="flex-1">
              <option value="">Add an end-user…</option>
              {candidates.map((u) => (
                <option key={u.user_id} value={u.user_id}>
                  {u.display_name}
                </option>
              ))}
            </Select>
            <Button variant="secondary" loading={adding} disabled={!addUserId} onClick={handleAdd} icon={<Plus className="h-4 w-4" />} />
          </div>
        )}

        {members === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-12" />
            <Skeleton className="h-12" />
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          {members?.map((m) => (
            <div key={m.user_id} className="flex items-center gap-3 rounded-xl px-2 py-2">
              <span className="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-surface-2 text-xs font-semibold text-text-muted">
                {m.display_name.slice(0, 1).toUpperCase()}
              </span>
              <span className="min-w-0 flex-1 truncate text-[15px] text-text">{m.display_name}</span>
              <button
                onClick={() => handleRemove(m.user_id)}
                disabled={removing === m.user_id}
                className="rounded-lg p-1.5 text-text-faint transition-colors duration-150 hover:bg-danger-soft hover:text-danger disabled:opacity-40"
                title="Remove from channel"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          ))}
        </div>
      </div>
    </Modal>
  );
}

// ---- Blocks ----

function BlocksTab({ appId }: { appId: number }) {
  const { session } = useSession();
  const [blocks, setBlocks] = useState<DashboardBlock[] | null>(null);
  const [names, setNames] = useState<Map<string, string>>(new Map());
  const [error, setError] = useState<string | null>(null);

  function refresh() {
    listDashboardBlocks(session.token, appId)
      .then(setBlocks)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
    listEndUsers(session.token, appId)
      .then((users) => setNames(new Map(users.map((u) => [u.user_id, u.display_name]))))
      .catch(() => {});
  }

  useEffect(refresh, [session.token, appId]);

  return (
    <div>
      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <Panel animate={false}>
        {blocks === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        {blocks?.length === 0 && <EmptyState text="No blocks for this app." />}
        <div className="flex flex-col gap-2">
          {blocks?.map((b, i) => (
            <motion.div
              key={`${b.blocker_user_id}-${b.blocked_user_id}`}
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.03 }}
              className="flex items-center gap-3.5 rounded-xl border border-border-soft px-4 py-3.5"
            >
              <BlockedUser userId={b.blocker_user_id} name={names.get(b.blocker_user_id)} />
              <ArrowRight className="h-4 w-4 shrink-0 text-text-faint" />
              <BlockedUser userId={b.blocked_user_id} name={names.get(b.blocked_user_id)} />
            </motion.div>
          ))}
        </div>
      </Panel>
    </div>
  );
}

function BlockedUser({ userId, name }: { userId: string; name: string | undefined }) {
  const label = name ?? `${userId.slice(0, 8)}…`;
  return (
    <div className="flex min-w-0 flex-1 items-center gap-2.5">
      <Avatar name={label} size="sm" />
      <span className="truncate text-[15px] text-text">{label}</span>
    </div>
  );
}

// ---- shared ----

function EmptyState({ text }: { text: ReactNode }) {
  return <p className="py-6 text-center text-[15px] text-text-muted">{text}</p>;
}
