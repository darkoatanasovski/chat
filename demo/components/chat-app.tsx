"use client";

import { useState } from "react";
import { MessageCircle, Settings } from "lucide-react";
import { REGION_ENDPOINTS } from "@/lib/regions";
import { clearProfile } from "@/lib/session";
import type { ChannelSummary, Profile } from "@/lib/types";
import { Avatar } from "@/components/ui";
import { ChannelList } from "@/components/channel-list";
import { ChatPanel } from "@/components/chat-panel";
import { MembersPanel } from "@/components/members-panel";
import { SettingsModal } from "@/components/settings-modal";

export function ChatApp({
  profile,
  onProfileChange,
  onSignedOut,
}: {
  profile: Profile;
  onProfileChange: (p: Profile) => void;
  onSignedOut: () => void;
}) {
  const [activeChannel, setActiveChannel] = useState<ChannelSummary | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [membersRefreshKey, setMembersRefreshKey] = useState(0);
  const apiBase = REGION_ENDPOINTS[profile.region].apiBase;

  return (
    <div className="grid h-full grid-cols-[260px_minmax(0,1fr)] lg:grid-cols-[260px_minmax(0,1fr)_280px]">
      <aside className="flex h-full min-h-0 flex-col border-r border-border-soft">
        <div className="flex shrink-0 items-center gap-2.5 border-b border-border-soft px-3.5 py-3">
          <Avatar name={profile.displayName} size="sm" accent />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium text-text">{profile.displayName}</div>
            <div className="truncate font-mono text-[11px] tracking-wide text-text-faint uppercase">
              {profile.region} &middot; {profile.tier}
            </div>
          </div>
          <button
            onClick={() => setSettingsOpen(true)}
            className="group grid h-8 w-8 shrink-0 place-items-center rounded-lg text-text-muted transition-all duration-200 hover:bg-white/[0.05] hover:text-text"
            title="Settings"
          >
            <Settings className="h-4 w-4 transition-transform duration-300 group-hover:rotate-45" />
          </button>
        </div>
        <div className="min-h-0 flex-1">
          <ChannelList
            apiBase={apiBase}
            profile={profile}
            activeChannelId={activeChannel?.channel_id ?? null}
            onSelect={setActiveChannel}
          />
        </div>
      </aside>

      <main className="flex h-full min-h-0 min-w-0 flex-col">
        {activeChannel ? (
          <ChatPanel
            key={activeChannel.channel_id}
            profile={profile}
            channel={activeChannel}
            onMessageDelivered={() => setMembersRefreshKey((k) => k + 1)}
          />
        ) : (
          <div className="animate-fade-in flex h-full flex-col items-center justify-center gap-2.5 text-center">
            <span className="grid h-12 w-12 place-items-center rounded-2xl bg-surface-2 text-text-faint">
              <MessageCircle className="h-5 w-5" />
            </span>
            <p className="text-sm text-text-muted">Pick a channel, or create one, to start testing.</p>
          </div>
        )}
      </main>

      <aside className="hidden h-full min-h-0 flex-col border-l border-border-soft lg:flex">
        {activeChannel ? (
          <MembersPanel
            apiBase={apiBase}
            token={profile.token}
            channelId={activeChannel.channel_id}
            activeUserId={profile.userId}
            refreshKey={membersRefreshKey}
          />
        ) : (
          <p className="p-4 text-xs text-text-faint">Select a channel to see its members.</p>
        )}
      </aside>

      <SettingsModal
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        profile={profile}
        onSwitchProfile={(p) => {
          onProfileChange(p);
          setActiveChannel(null);
          setSettingsOpen(false);
        }}
        onSignOut={() => {
          clearProfile();
          setSettingsOpen(false);
          onSignedOut();
        }}
      />
    </div>
  );
}
