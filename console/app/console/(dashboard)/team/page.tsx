"use client";

import { useState, type FormEvent } from "react";
import { motion } from "framer-motion";
import { Check, Clock, Copy, Mail, Plus, Trash2, Users } from "lucide-react";
import { ApiError } from "@/lib/api";
import { useCreateInviteMutation, useInvitesQuery, useRemoveTeamMemberMutation, useTeamQuery } from "@/lib/queries";
import type { Invite } from "@/lib/types";
import { useSession } from "@/components/shell";
import { useToast } from "@/components/toast";
import { Avatar, Badge, Button, ErrorBanner, Input, Label, Modal, Panel, Select, Skeleton } from "@/components/ui";

export default function TeamPage() {
  return (
      <TeamView />
  );
}

function TeamView() {
  const { session } = useSession();
  const toast = useToast();
  const isOwner = session.user.role === "owner";

  const teamQuery = useTeamQuery(session.token);
  const team = teamQuery.data ?? null;
  const error = teamQuery.error ? (teamQuery.error instanceof ApiError ? teamQuery.error.message : String(teamQuery.error)) : null;
  const { data: invitesData } = useInvitesQuery(session.token, isOwner);
  const invites = invitesData ?? null;

  const [inviteOpen, setInviteOpen] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const removeTeamMemberMutation = useRemoveTeamMemberMutation(session.token);

  async function handleRemove(userId: string) {
    setRemoving(userId);
    try {
      await removeTeamMemberMutation.mutateAsync(userId);
      toast("success", "Team member removed");
    } catch (err) {
      toast("error", err instanceof ApiError ? err.message : String(err));
    } finally {
      setRemoving(null);
    }
  }

  return (
    <div>
      <div className="mb-8 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-text">Team</h1>
          <p className="mt-1.5 text-[15px] text-text-muted">Everyone with access to {session.org.name}.</p>
        </div>
        {isOwner && (
          <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setInviteOpen(true)}>
            Invite teammate
          </Button>
        )}
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      <Panel animate={false} className="mb-6">
        <h2 className="mb-5 flex items-center gap-2.5 text-base font-semibold text-text">
          <Users className="h-5 w-5 text-text-muted" />
          Members
        </h2>
        {team === null && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-16" />
            <Skeleton className="h-16" />
          </div>
        )}
        <div className="flex flex-col gap-1">
          {team?.map((member, i) => {
            const isSelf = member.user_id === session.user.user_id;
            return (
              <motion.div
                key={member.user_id}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.04 }}
                className="flex items-center gap-3.5 rounded-xl px-3 py-3 hover:bg-surface-2/60"
              >
                <Avatar name={member.email} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[15px] text-text">
                    {member.email} {isSelf && <span className="text-text-faint">(you)</span>}
                  </div>
                  <div className="text-xs text-text-faint">Joined {new Date(member.created_at).toLocaleDateString()}</div>
                </div>
                <Badge tone={member.role === "owner" ? "accent" : "default"}>{member.role}</Badge>
                {isOwner && !isSelf && (
                  <Button
                    variant="ghost"
                    className="px-2.5 text-danger hover:bg-danger-soft"
                    loading={removing === member.user_id}
                    onClick={() => handleRemove(member.user_id)}
                    icon={<Trash2 className="h-4 w-4" />}
                  />
                )}
              </motion.div>
            );
          })}
        </div>
      </Panel>

      {isOwner && invites && invites.length > 0 && (
        <Panel animate={false}>
          <h2 className="mb-5 flex items-center gap-2.5 text-base font-semibold text-text">
            <Clock className="h-5 w-5 text-text-muted" />
            Pending invites
          </h2>
          <div className="flex flex-col gap-1">
            {invites.map((inv, i) => (
              <motion.div
                key={inv.invite_id}
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.04 }}
                className="flex items-center gap-3.5 rounded-xl px-3 py-3"
              >
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-surface-2 text-text-faint">
                  <Mail className="h-4 w-4" />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[15px] text-text">{inv.email}</div>
                  <div className="text-xs text-text-faint">Expires {new Date(inv.expires_at).toLocaleDateString()}</div>
                </div>
                <Badge>{inv.role}</Badge>
              </motion.div>
            ))}
          </div>
        </Panel>
      )}

      <InviteModal open={inviteOpen} onClose={() => setInviteOpen(false)} />
    </div>
  );
}

function InviteModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { session } = useSession();
  const toast = useToast();
  const createInviteMutation = useCreateInviteMutation(session.token);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<"member" | "owner">("member");
  const [error, setError] = useState<string | null>(null);
  const [invite, setInvite] = useState<Invite | null>(null);
  const [copied, setCopied] = useState(false);

  function reset() {
    setEmail("");
    setRole("member");
    setError(null);
    setInvite(null);
    setCopied(false);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      const inv = await createInviteMutation.mutateAsync({ email: email.trim(), role });
      setInvite(inv);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }

  const inviteLink = invite ? `${window.location.origin}/console/invite/${invite.token}` : "";

  return (
    <Modal
      open={open}
      onClose={() => {
        onClose();
        setTimeout(reset, 200);
      }}
      title={invite ? "Invite sent" : "Invite a teammate"}
      icon={<Users className="h-4 w-4 text-accent" />}
      widthClass="max-w-lg"
    >
      {!invite ? (
        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          <div>
            <Label>Email</Label>
            <Input type="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="teammate@company.com" />
          </div>
          <div>
            <Label>Role</Label>
            <Select value={role} onChange={(e) => setRole(e.target.value as "member" | "owner")}>
              <option value="member">Member</option>
              <option value="owner">Owner</option>
            </Select>
          </div>
          <Button type="submit" variant="primary" loading={createInviteMutation.isPending} className="justify-center">
            Create invite
          </Button>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </form>
      ) : (
        <div className="flex flex-col gap-4">
          <ErrorBanner>
            <span className="text-text">There&apos;s no email sending in this demo — share this link with {invite.email} yourself.</span>
          </ErrorBanner>
          <div>
            <Label>Invite link</Label>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate rounded-lg border border-border bg-surface-2 px-3 py-2 font-mono text-xs text-text">{inviteLink}</code>
              <Button
                variant="secondary"
                onClick={() => {
                  navigator.clipboard
                    .writeText(inviteLink)
                    .then(() => {
                      setCopied(true);
                      setTimeout(() => setCopied(false), 1500);
                    })
                    .catch(() => toast("error", "Couldn't copy automatically — select and copy the link manually."));
                }}
                icon={copied ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
              />
            </div>
          </div>
          <Button
            variant="primary"
            className="justify-center"
            onClick={() => {
              onClose();
              setTimeout(reset, 200);
            }}
          >
            Done
          </Button>
        </div>
      )}
    </Modal>
  );
}
