"use client";

import { useEffect, useState } from "react";
import { Plus, Users } from "lucide-react";
import { addMember, listMembers, ApiError } from "@/lib/api";
import { loadContacts, loadRoster } from "@/lib/session";
import type { Member } from "@/lib/types";
import { Avatar, cx, ErrorBanner } from "@/components/ui";

interface Candidate {
  userId: string;
  displayName: string;
}

export function MembersPanel({
  apiBase,
  token,
  channelId,
  activeUserId,
  refreshKey,
}: {
  apiBase: string;
  token: string;
  channelId: string;
  activeUserId: string;
  /** Bump this from the parent (e.g. after a message arrives from an
   * unfamiliar sender) to force a re-fetch of the member list. */
  refreshKey?: number;
}) {
  const [members, setMembers] = useState<Member[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [addingId, setAddingId] = useState<string | null>(null);

  async function refresh() {
    try {
      const list = await listMembers(apiBase, token, channelId);
      setMembers(list);
    } catch (err) {
      setError(err instanceof ApiError ? `${err.status}: ${err.message}` : String(err));
    }
  }

  useEffect(() => {
    setMembers(null);
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId, refreshKey]);

  async function handleAdd(candidate: Candidate) {
    setError(null);
    setAddingId(candidate.userId);
    try {
      await addMember(apiBase, token, channelId, candidate.userId);
      await refresh();
    } catch (err) {
      setError(err instanceof ApiError ? `${err.status}: ${err.message}` : String(err));
    } finally {
      setAddingId(null);
    }
  }

  const memberIds = new Set((members ?? []).map((m) => m.user_id));
  const candidates: Candidate[] = [
    ...loadRoster()
      .filter((p) => p.userId !== activeUserId)
      .map((p) => ({ userId: p.userId, displayName: p.displayName })),
    ...loadContacts(),
  ].filter((c, i, arr) => arr.findIndex((x) => x.userId === c.userId) === i && !memberIds.has(c.userId));

  return (
    <div className="flex h-full flex-col gap-5 overflow-y-auto p-4">
      <div>
        <div className="mb-2 flex items-center gap-2 px-1">
          <Users className="h-3.5 w-3.5 text-text-muted" strokeWidth={2.25} />
          <h3 className="text-[11px] font-semibold tracking-wide text-text-muted uppercase">
            In this channel {members ? `(${members.length})` : ""}
          </h3>
        </div>
        <ul className="flex flex-col gap-0.5">
          {members === null && <li className="px-1 py-1 text-xs text-text-faint">Loading&hellip;</li>}
          {members?.map((m, i) => (
            <li
              key={m.user_id}
              className="animate-fade-in-up flex items-center gap-2.5 rounded-lg px-2 py-1.5 transition-colors duration-150 hover:bg-white/[0.03]"
              style={{ animationDelay: `${i * 30}ms` }}
            >
              <Avatar name={m.display_name} size="sm" online accent={m.user_id === activeUserId} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm text-text">
                  {m.display_name}
                  {m.user_id === activeUserId && <span className="text-text-faint"> (you)</span>}
                </div>
              </div>
            </li>
          ))}
        </ul>
      </div>

      {candidates.length > 0 && (
        <div>
          <div className="mb-2 flex items-center gap-2 px-1">
            <Plus className="h-3.5 w-3.5 text-text-muted" strokeWidth={2.25} />
            <h3 className="text-[11px] font-semibold tracking-wide text-text-muted uppercase">Not in this channel</h3>
          </div>
          <ul className="flex flex-col gap-0.5">
            {candidates.map((c, i) => (
              <li key={c.userId} className="animate-fade-in-up" style={{ animationDelay: `${i * 30}ms` }}>
                <button
                  onClick={() => handleAdd(c)}
                  disabled={addingId === c.userId}
                  className="group flex w-full items-center gap-2.5 rounded-lg px-2 py-1.5 text-left transition-all duration-150 hover:bg-white/[0.04] active:scale-[0.98] disabled:cursor-wait disabled:opacity-60"
                >
                  <Avatar name={c.displayName} size="sm" online={false} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm text-text-muted group-hover:text-text">{c.displayName}</div>
                  </div>
                  <span
                    className={cx(
                      "grid h-6 w-6 shrink-0 place-items-center rounded-full border border-border text-text-faint transition-all duration-150 group-hover:scale-110 group-hover:border-accent group-hover:text-accent",
                      addingId === c.userId && "animate-spin border-accent text-accent"
                    )}
                  >
                    <Plus className="h-3.5 w-3.5" />
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {members !== null && members.length <= 1 && candidates.length === 0 && (
        <p className="px-1 text-xs text-text-faint">
          No one else known yet — add a test user or paste a user_id from Settings.
        </p>
      )}

      {error && <ErrorBanner>{error}</ErrorBanner>}
    </div>
  );
}
