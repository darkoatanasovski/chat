"use client";

import { useState, type FormEvent } from "react";
import { motion } from "framer-motion";
import { Boxes, Check, Copy, KeyRound, Plus } from "lucide-react";
import { ApiError } from "@/lib/api";
import { useAppsMessagesDailyQuery, useAppsQuery, useCreateAppMutation } from "@/lib/queries";
import type { CreatedApp } from "@/lib/types";
import { ConsoleShell, useSession } from "@/components/shell";
import { useToast } from "@/components/toast";
import { Button, ErrorBanner, Input, Label, Modal, Panel, Skeleton, Sparkline } from "@/components/ui";

// Matches the backend's dashboardDailyWindowDays (cmd/api/handlers_dashboard.go)
// — the fallback shape for an app the /messages/daily response hasn't
// returned yet (still loading, or genuinely has no channels), so a card
// always has a full week of zeros to chart rather than nothing to chart.
const DAILY_WINDOW = 7;
const ZERO_DAILY = Array<number>(DAILY_WINDOW).fill(0);

export default function AppsPage() {
  return (
    <ConsoleShell>
      <AppsView />
    </ConsoleShell>
  );
}

function AppsView() {
  const { session } = useSession();
  const toast = useToast();
  const [createOpen, setCreateOpen] = useState(false);

  const appsQuery = useAppsQuery(session.token, session.org.org_id);
  const apps = appsQuery.data ?? null;
  const error = appsQuery.error ? (appsQuery.error instanceof ApiError ? appsQuery.error.message : String(appsQuery.error)) : null;

  // Independent of the apps list itself — a slower or failed load here
  // shouldn't block the grid, it just means cards render with zeroed
  // stats until it resolves.
  const { data: dailyRes } = useAppsMessagesDailyQuery(session.token);
  const messagesByApp = dailyRes ? new Map(dailyRes.apps.map((a) => [a.app_id, a])) : null;

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
            <Skeleton key={i} className="h-56" />
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
          {apps.map((app, i) => {
            const stats = messagesByApp?.get(app.app_id);
            const daily = stats?.daily ?? ZERO_DAILY;
            return (
              <motion.a
                key={app.app_id}
                href={`/console/apps/${app.app_id}`}
                initial={{ opacity: 0, y: 10 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ delay: i * 0.05, duration: 0.3, ease: [0.16, 1, 0.3, 1] }}
                className="group flex flex-col rounded-2xl border border-border bg-surface p-6 transition-colors duration-150 hover:border-accent/40"
              >
                <div className="mb-4 flex items-center justify-between">
                  <span className="grid h-11 w-11 place-items-center rounded-full bg-accent-soft text-accent">
                    <KeyRound className="h-5 w-5" />
                  </span>
                  <span className="font-mono text-xs text-text-faint">#{app.app_id}</span>
                </div>
                <div className="truncate text-[15px] font-semibold text-text">{app.name}</div>
                <div className="mt-1 text-sm text-text-faint">Created {new Date(app.created_at).toLocaleDateString()}</div>

                <div className="mt-5 flex items-baseline gap-5 border-t border-border-soft pt-4">
                  <div>
                    <div className="text-lg font-semibold text-text">{(stats?.total ?? 0).toLocaleString()}</div>
                    <div className="text-xs text-text-faint">messages</div>
                  </div>
                  <div>
                    <div className="text-lg font-semibold text-text">{(stats?.today ?? 0).toLocaleString()}</div>
                    <div className="text-xs text-text-faint">today</div>
                  </div>
                </div>
                <div className="mt-3">
                  <Sparkline values={daily} />
                </div>
              </motion.a>
            );
          })}
        </div>
      )}

      <CreateAppModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => toast("success", "App created")}
      />
    </div>
  );
}

function CreateAppModal({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: () => void }) {
  const { session } = useSession();
  const createAppMutation = useCreateAppMutation(session.token, session.org.org_id);
  const [name, setName] = useState("");
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
    try {
      const app = await createAppMutation.mutateAsync(name.trim());
      setCreated(app);
      onCreated();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
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
          <Button type="submit" variant="primary" loading={createAppMutation.isPending} className="justify-center">
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
