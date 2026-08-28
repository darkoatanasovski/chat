"use client";

import { useEffect, useState, type FormEvent } from "react";
import { motion } from "framer-motion";
import { Boxes, Check, Copy, KeyRound, Plus } from "lucide-react";
import { createApp, listApps, ApiError } from "@/lib/api";
import type { AppSummary, CreatedApp } from "@/lib/types";
import { DashboardShell, useSession } from "@/components/shell";
import { useToast } from "@/components/toast";
import { Button, ErrorBanner, Input, Label, Modal, Panel, Skeleton } from "@/components/ui";

export default function AppsPage() {
  return (
    <DashboardShell>
      <AppsView />
    </DashboardShell>
  );
}

function AppsView() {
  const { session } = useSession();
  const toast = useToast();
  const [apps, setApps] = useState<AppSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  function refresh() {
    listApps(session.token, session.org.org_id)
      .then(setApps)
      .catch((err) => setError(err instanceof ApiError ? err.message : String(err)));
  }

  useEffect(refresh, [session.token, session.org.org_id]);

  return (
    <div>
      <div className="mb-8 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-text">Apps</h1>
          <p className="mt-1.5 text-[15px] text-text-muted">Each app is an isolated chat instance with its own end-users and API keys.</p>
        </div>
        <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setCreateOpen(true)}>
          Create app
        </Button>
      </div>

      {error && (
        <div className="mb-5">
          <ErrorBanner>{error}</ErrorBanner>
        </div>
      )}

      {apps === null && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-32" />
          ))}
        </div>
      )}

      {apps?.length === 0 && (
        <Panel className="flex flex-col items-center gap-4 py-16 text-center">
          <span className="grid h-14 w-14 place-items-center rounded-full bg-surface-2 text-text-faint">
            <Boxes className="h-6 w-6" />
          </span>
          <p className="text-[15px] text-text-muted">No apps yet — create your first one to get an API key.</p>
          <Button variant="primary" icon={<Plus className="h-4 w-4" />} onClick={() => setCreateOpen(true)}>
            Create app
          </Button>
        </Panel>
      )}

      {apps && apps.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {apps.map((app, i) => (
            <motion.a
              key={app.app_id}
              href={`/apps/${app.app_id}`}
              initial={{ opacity: 0, y: 10 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ delay: i * 0.05, duration: 0.3, ease: [0.16, 1, 0.3, 1] }}
              className="group rounded-2xl border border-border bg-surface p-6 transition-colors duration-150 hover:border-accent/40"
            >
              <div className="mb-4 flex items-center justify-between">
                <span className="grid h-11 w-11 place-items-center rounded-full bg-accent-soft text-accent">
                  <KeyRound className="h-5 w-5" />
                </span>
                <span className="font-mono text-xs text-text-faint">#{app.app_id}</span>
              </div>
              <div className="truncate text-[15px] font-semibold text-text">{app.name}</div>
              <div className="mt-1 text-sm text-text-faint">Created {new Date(app.created_at).toLocaleDateString()}</div>
            </motion.a>
          ))}
        </div>
      )}

      <CreateAppModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          refresh();
          toast("success", "App created");
        }}
      />
    </div>
  );
}

function CreateAppModal({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: () => void }) {
  const { session } = useSession();
  const [name, setName] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<CreatedApp | null>(null);
  const [copiedKey, setCopiedKey] = useState(false);
  const [copiedSecret, setCopiedSecret] = useState(false);

  function reset() {
    setName("");
    setError(null);
    setCreated(null);
    setCopiedKey(false);
    setCopiedSecret(false);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const app = await createApp(session.token, session.org.org_id, name.trim());
      setCreated(app);
      onCreated();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  function copy(text: string, which: "key" | "secret") {
    navigator.clipboard.writeText(text).then(() => {
      if (which === "key") {
        setCopiedKey(true);
        setTimeout(() => setCopiedKey(false), 1500);
      } else {
        setCopiedSecret(true);
        setTimeout(() => setCopiedSecret(false), 1500);
      }
    }).catch(() => {
      setError("Couldn't copy automatically — select and copy the text manually.");
    });
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        onClose();
        setTimeout(reset, 200);
      }}
      title={created ? "App created" : "Create a new app"}
      icon={<Boxes className="h-4 w-4 text-accent" />}
      widthClass="max-w-lg"
    >
      {!created ? (
        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          <div>
            <Label>App name</Label>
            <Input required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Production" />
          </div>
          <Button type="submit" variant="primary" loading={loading} className="justify-center">
            Create app
          </Button>
          {error && <ErrorBanner>{error}</ErrorBanner>}
        </form>
      ) : (
        <div className="flex flex-col gap-4">
          <ErrorBanner>
            <span className="text-text">
              This secret is shown <strong>once</strong> — copy it now. You can always generate a new credential later from the app page.
            </span>
          </ErrorBanner>
          <div>
            <Label>API key</Label>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate rounded-lg border border-border bg-surface-2 px-3 py-2 font-mono text-xs text-text">
                {created.credential.key}
              </code>
              <Button variant="secondary" onClick={() => copy(created.credential.key, "key")} icon={copiedKey ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />} />
            </div>
          </div>
          <div>
            <Label>API secret</Label>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate rounded-lg border border-border bg-surface-2 px-3 py-2 font-mono text-xs text-text">
                {created.credential.secret}
              </code>
              <Button
                variant="secondary"
                onClick={() => copy(created.credential.secret ?? "", "secret")}
                icon={copiedSecret ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
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
