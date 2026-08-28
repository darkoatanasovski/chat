"use client";

import { useState } from "react";
import { Check, Copy, Gauge, Layers, LogOut, Settings, Trash2, UserPlus, Users } from "lucide-react";
import { createUser, ApiError } from "@/lib/api";
import { REGIONS, REGION_ENDPOINTS, type Region } from "@/lib/regions";
import { addContact, addToRoster, loadContacts, loadRoster, removeContact, removeFromRoster, setActiveProfile } from "@/lib/session";
import { credentialsForTier, TIER_LIMITS, TIERS, type Tier } from "@/lib/tiers";
import type { Profile } from "@/lib/types";
import { Avatar, Badge, Button, cx, ErrorBanner, Input, Modal, Select } from "@/components/ui";

export function SettingsModal({
  open,
  onClose,
  profile,
  onSwitchProfile,
  onSignOut,
}: {
  open: boolean;
  onClose: () => void;
  profile: Profile;
  onSwitchProfile: (p: Profile) => void;
  onSignOut: () => void;
}) {
  const [roster, setRoster] = useState(loadRoster);
  const [contacts, setContacts] = useState(loadContacts);
  const [newName, setNewName] = useState("");
  const [newRegion, setNewRegion] = useState<Region>(profile.region);
  const [newTier, setNewTier] = useState<Tier>("FREE");
  const [creating, setCreating] = useState(false);
  const [contactId, setContactId] = useState("");
  const [contactLabel, setContactLabel] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  async function handleCreateUser(e: React.FormEvent) {
    e.preventDefault();
    if (!newName.trim()) return;
    setError(null);
    setCreating(true);
    try {
      const credentials = credentialsForTier(newTier);
      if (!credentials) {
        setError(`No demo app credentials configured for tier ${newTier} (set NEXT_PUBLIC_DEMO_APP_CREDENTIALS_${newTier} — see deploy/seed.sh output)`);
        setCreating(false);
        return;
      }
      const apiBase = REGION_ENDPOINTS[newRegion].apiBase;
      const u = await createUser(apiBase, newName.trim(), newRegion, credentials);
      const created: Profile = {
        userId: u.user_id,
        displayName: u.display_name,
        region: newRegion,
        tier: u.tier,
        token: u.token,
      };
      // Stash in the roster without switching the active session — this is
      // for populating test identities you can invite, not signing in as
      // them.
      setRoster(addToRoster(created));
      setNewName("");
    } catch (err) {
      setError(err instanceof ApiError ? `${err.status}: ${err.message}` : String(err));
    } finally {
      setCreating(false);
    }
  }

  function handleAddContact(e: React.FormEvent) {
    e.preventDefault();
    const id = contactId.trim();
    if (!id) return;
    addContact({ userId: id, displayName: contactLabel.trim() || id.slice(0, 8) });
    setContacts(loadContacts());
    setContactId("");
    setContactLabel("");
  }

  function copyUserId() {
    navigator.clipboard.writeText(profile.userId).then(
      () => flash(),
      () => flash()
    );
    function flash() {
      setCopied(true);
      setTimeout(() => setCopied(false), 1400);
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="Settings" icon={<Settings className="h-4 w-4 text-accent" />} widthClass="max-w-lg">
      <div className="flex flex-col gap-7">
        <section className="flex items-center gap-3">
          <Avatar name={profile.displayName} size="md" accent />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium text-text">{profile.displayName}</div>
            <div className="truncate font-mono text-[11px] tracking-wide text-text-faint uppercase">
              {profile.region} &middot; {profile.tier}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            <button
              onClick={copyUserId}
              className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-1 font-mono text-[11px] text-text-faint transition-colors duration-150 hover:border-text-faint hover:text-text-muted"
              title="Copy your user_id"
            >
              {copied ? <Check className="h-3 w-3 text-success" /> : <Copy className="h-3 w-3" />}
              {profile.userId.slice(0, 8)}&hellip;
            </button>
            <Button variant="ghost" icon={<LogOut className="h-3.5 w-3.5" />} onClick={onSignOut} className="px-2 text-xs">
              Sign out
            </Button>
          </div>
        </section>

        <section>
          <div className="mb-2.5 flex items-center gap-2">
            <Users className="h-4 w-4 text-text-muted" strokeWidth={2.25} />
            <h3 className="text-sm font-semibold text-text">Your test users</h3>
          </div>
          {roster.length === 0 && <p className="text-sm text-text-muted">None yet — create one below.</p>}
          <ul className="flex flex-col gap-1">
            {roster.map((p) => (
              <li key={p.userId} className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 hover:bg-white/[0.03]">
                <Avatar name={p.displayName} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm text-text">{p.displayName}</div>
                  <div className="font-mono text-[11px] text-text-faint">{p.region}</div>
                </div>
                {p.userId === profile.userId ? (
                  <Badge tone="accent">active</Badge>
                ) : (
                  <Button
                    variant="secondary"
                    className="px-2.5 py-1 text-xs"
                    onClick={() => {
                      setActiveProfile(p);
                      onSwitchProfile(p);
                    }}
                  >
                    Switch to
                  </Button>
                )}
                {p.userId !== profile.userId && (
                  <button
                    onClick={() => {
                      removeFromRoster(p.userId);
                      setRoster(loadRoster());
                    }}
                    className="rounded-md p-1 text-text-faint transition-colors duration-150 hover:text-danger"
                    title="Remove from this list"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                )}
              </li>
            ))}
          </ul>

          {/* Two rows, not one crammed row: this lives inside a fixed-width
             modal, not the full viewport, so a breakpoint-based flex-row
             (e.g. sm:flex-row) doesn't actually guarantee these 4 controls
             fit — region labels like "Asia (Singapore)" alone are wide
             enough to force horizontal overflow of the modal itself. */}
          <form onSubmit={handleCreateUser} className="mt-3 flex flex-col gap-2">
            <Input placeholder="new test user name" value={newName} onChange={(e) => setNewName(e.target.value)} />
            <div className="grid grid-cols-2 gap-2">
              <Select className="min-w-0" value={newRegion} onChange={(e) => setNewRegion(e.target.value as Region)}>
                {REGIONS.map((r) => (
                  <option key={r} value={r}>
                    {REGION_ENDPOINTS[r].label}
                  </option>
                ))}
              </Select>
              <Select className="min-w-0" value={newTier} onChange={(e) => setNewTier(e.target.value as Tier)}>
                {TIERS.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            </div>
            <Button type="submit" variant="primary" loading={creating} icon={<UserPlus className="h-3.5 w-3.5" />}>
              Create test user
            </Button>
          </form>
        </section>

        <section>
          <div className="mb-2.5 flex items-center gap-2">
            <UserPlus className="h-4 w-4 text-text-muted" strokeWidth={2.25} />
            <h3 className="text-sm font-semibold text-text">Add a known user by id</h3>
          </div>
          <p className="mb-2.5 text-xs text-text-muted">
            From another browser/session — paste their user_id to be able to invite them from the members panel.
          </p>
          {contacts.length > 0 && (
            <ul className="mb-2.5 flex flex-col gap-1">
              {contacts.map((c) => (
                <li key={c.userId} className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 hover:bg-white/[0.03]">
                  <Avatar name={c.displayName} size="sm" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm text-text">{c.displayName}</div>
                    <div className="truncate font-mono text-[11px] text-text-faint">{c.userId}</div>
                  </div>
                  <button
                    onClick={() => {
                      removeContact(c.userId);
                      setContacts(loadContacts());
                    }}
                    className="rounded-md p-1 text-text-faint transition-colors duration-150 hover:text-danger"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </li>
              ))}
            </ul>
          )}
          <form onSubmit={handleAddContact} className="flex flex-col gap-2 sm:flex-row">
            <Input
              className="min-w-0 sm:flex-1"
              placeholder="user_id"
              value={contactId}
              onChange={(e) => setContactId(e.target.value)}
            />
            <Input
              className="min-w-0 sm:w-36"
              placeholder="label (optional)"
              value={contactLabel}
              onChange={(e) => setContactLabel(e.target.value)}
            />
            <Button type="submit" variant="secondary">
              Add
            </Button>
          </form>
        </section>

        <section>
          <div className="mb-2.5 flex items-center gap-2">
            <Gauge className="h-4 w-4 text-text-muted" strokeWidth={2.25} />
            <h3 className="text-sm font-semibold text-text">Tier limits</h3>
          </div>
          <div className="flex flex-col gap-1.5">
            {TIERS.map((t) => (
              <div
                key={t}
                className={cx(
                  "flex items-center gap-3 rounded-lg border px-3 py-2 transition-colors duration-150",
                  t === profile.tier ? "border-accent/40 bg-accent-soft" : "border-border-soft bg-bg/40"
                )}
              >
                <span className={cx("w-20 shrink-0 font-mono text-xs font-semibold", t === profile.tier ? "text-accent" : "text-text-muted")}>
                  {t}
                </span>
                <span className="font-mono text-[11px] text-text-faint">
                  {TIER_LIMITS[t].channels} ch &middot; {TIER_LIMITS[t].members} members &middot;{" "}
                  {TIER_LIMITS[t].messagesPerMinute} msg/min
                </span>
              </div>
            ))}
          </div>
          <p className="mt-2 flex items-center gap-1.5 text-xs text-text-muted">
            <Layers className="h-3 w-3 shrink-0" />
            Pick a tier when creating a user above — there&apos;s no upgrade path for an existing user, by design.
          </p>
        </section>

        {error && <ErrorBanner>{error}</ErrorBanner>}
      </div>
    </Modal>
  );
}
