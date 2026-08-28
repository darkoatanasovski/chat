"use client";

import { useEffect, useState } from "react";
import { Hash, Plus } from "lucide-react";
import { createChannel, listMyChannels, ApiError } from "@/lib/api";
import type { ChannelSummary, Profile } from "@/lib/types";
import { Button, ErrorBanner, Input, cx } from "@/components/ui";

export function ChannelList({
  apiBase,
  profile,
  activeChannelId,
  onSelect,
  onChannelsLoaded,
  refreshKey,
}: {
  apiBase: string;
  profile: Profile;
  activeChannelId: string | null;
  onSelect: (channel: ChannelSummary) => void;
  onChannelsLoaded?: (channels: ChannelSummary[]) => void;
  refreshKey?: number;
}) {
  const [channels, setChannels] = useState<ChannelSummary[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function refresh() {
    try {
      const list = await listMyChannels(apiBase, profile.token);
      setChannels(list);
      onChannelsLoaded?.(list);
    } catch (err) {
      setError(err instanceof ApiError ? `${err.status}: ${err.message}` : String(err));
    }
  }

  useEffect(() => {
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profile.userId, refreshKey]);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!newName.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const c = await createChannel(apiBase, profile.token, newName.trim());
      setNewName("");
      setCreating(false);
      await refresh();
      onSelect({ channel_id: c.channel_id, name: c.name, home_region: c.home_region, last_message_sequence: 0 });
    } catch (err) {
      setError(err instanceof ApiError ? `${err.status}: ${err.message}` : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between px-4 pt-4 pb-2">
        <h2 className="text-sm font-semibold text-text">Chats</h2>
        <button
          onClick={() => setCreating((v) => !v)}
          className={cx(
            "grid h-7 w-7 place-items-center rounded-full transition-all duration-150",
            creating ? "bg-accent text-white rotate-45" : "bg-surface-2 text-text-muted hover:text-text"
          )}
          title="New channel"
        >
          <Plus className="h-4 w-4" />
        </button>
      </div>

      {creating && (
        <form onSubmit={handleCreate} className="animate-fade-in-up flex gap-1.5 px-4 pb-3">
          <Input
            autoFocus
            className="flex-1 py-1.5 text-xs"
            placeholder="channel name"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
          />
          <Button type="submit" variant="primary" loading={busy} className="px-2.5 py-1.5 text-xs">
            Add
          </Button>
        </form>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {channels === null && <p className="px-2 py-2 text-xs text-text-faint">Loading&hellip;</p>}
        {channels?.length === 0 && (
          <p className="px-2 py-2 text-xs text-text-faint">No channels yet — create one above.</p>
        )}
        <ul className="flex flex-col gap-0.5">
          {channels?.map((c, i) => {
            const active = c.channel_id === activeChannelId;
            return (
              <li key={c.channel_id} className="animate-fade-in-up" style={{ animationDelay: `${Math.min(i * 25, 200)}ms` }}>
                <button
                  onClick={() => onSelect(c)}
                  className={cx(
                    "relative flex w-full items-center gap-2 rounded-lg py-2 pr-2.5 pl-3 text-left transition-all duration-150 active:scale-[0.98]",
                    active ? "bg-accent-soft text-text" : "text-text-muted hover:bg-white/[0.04] hover:text-text"
                  )}
                >
                  {active && (
                    <span className="absolute top-1/2 left-0.5 h-4 w-1 -translate-y-1/2 rounded-full bg-accent" />
                  )}
                  <Hash className={cx("h-3.5 w-3.5 shrink-0 transition-colors duration-150", active ? "text-accent" : "text-text-faint")} />
                  <span className="truncate text-sm">{c.name}</span>
                  <span className="ml-auto shrink-0 font-mono text-[10px] text-text-faint">{c.home_region}</span>
                </button>
              </li>
            );
          })}
        </ul>
      </div>

      {error && (
        <div className="px-4 pb-3">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}
    </div>
  );
}
