"use client";

// One feature's form, Run button, result, and code snippets — rendered for
// whichever catalog entry (lib/playground/features.ts) is selected.
import { useEffect, useState } from "react";
import { Check, Copy, ExternalLink, Play, Settings2 } from "lucide-react";
import type { AppSummary } from "@/lib/types";
import { CAPABILITY_GROUPS } from "@/lib/capabilities";
import type { RequestRecord, RequestSpec } from "@/lib/playground/client";
import type { Feature, FeatureContext, Field, RecentIds, Values } from "@/lib/playground/features";
import { httpSnippet, SNIPPET_LANGS, wsSnippet, type SnippetLang } from "@/lib/playground/snippets";
import { useToast } from "@/components/toast";
import { Badge, Button, Input, Label, Panel, Select, Switch, Textarea, cx } from "@/components/ui";

const SNIPPET_LANG_KEY = "chat-console:playground:snippet-lang";

export interface UserOption {
  userId: string;
  label: string;
}

function capabilityLabel(key: string): string {
  for (const g of CAPABILITY_GROUPS) {
    const hit = g.items.find((i) => i.key === key);
    if (hit) return hit.label;
  }
  return key;
}

export function FeaturePanel({
  feature,
  app,
  values,
  onChange,
  onRun,
  running,
  lastRecord,
  ctx,
  userOptions,
  recent,
  hasActor,
}: {
  feature: Feature;
  app: AppSummary | null;
  values: Values;
  onChange: (values: Values) => void;
  onRun: () => void;
  running: boolean;
  lastRecord: RequestRecord | null;
  ctx: FeatureContext;
  userOptions: UserOption[];
  recent: RecentIds;
  hasActor: boolean;
}) {
  const channelScoped = feature.channelScoped !== false;
  const blocked = !hasActor ? "Add an actor to run this." : channelScoped && !ctx.channelId ? "Select a channel first." : null;

  const capOn = feature.capability ? app?.channel_capabilities[feature.capability] ?? null : null;
  const editOn = feature.requiresEdit ? app?.message_edit_enabled ?? null : null;

  const missing = feature.fields.filter((f) => f.required && f.type !== "checkbox" && !(values[f.key] ?? "").toString().trim());

  return (
    <div className="flex flex-col gap-5">
      <Panel animate={false} className="p-6">
        <div className="mb-1 flex flex-wrap items-center gap-2">
          <h2 className="text-lg font-semibold text-text">{feature.label}</h2>
          {feature.capability && app && (
            <a
              href={`/console/apps/${app.app_id}?tab=settings&setting=${feature.capability}`}
              title="Open this capability in the app's settings"
            >
              <Badge tone={capOn ? "success" : "warning"} icon={<Settings2 className="h-3 w-3" />} className="cursor-pointer hover:opacity-80">
                {capabilityLabel(feature.capability)}: {capOn ? "on" : "off"}
              </Badge>
            </a>
          )}
          {feature.requiresEdit && app && (
            <a href={`/console/apps/${app.app_id}?tab=settings&setting=message_edit_enabled`} title="Open Message Editing in the app's settings">
              <Badge tone={editOn ? "success" : "warning"} icon={<Settings2 className="h-3 w-3" />} className="cursor-pointer hover:opacity-80">
                Message Editing: {editOn ? "on" : "off"}
              </Badge>
            </a>
          )}
          {!feature.build && <Badge tone="default">observe only</Badge>}
        </div>
        <p className="text-[15px] leading-relaxed text-text-muted">{feature.description}</p>
        {(capOn === false || editOn === false) && (
          <p className="mt-2 text-sm text-warning">
            Switched off for this app — running it shows the API&apos;s real 403. Turn it on from the badge above to see it work.
          </p>
        )}

        {feature.fields.length > 0 && (
          <div className="mt-5 grid gap-4 border-t border-border-soft pt-5 sm:grid-cols-2">
            {feature.fields.map((field) => (
              <FieldInput
                key={field.key}
                field={field}
                value={values[field.key]}
                onChange={(v) => onChange({ ...values, [field.key]: v })}
                userOptions={userOptions}
                recent={recent}
                wide={field.type === "textarea" || field.type === "json"}
              />
            ))}
          </div>
        )}

        {feature.build && (
          <div className="mt-5 flex flex-wrap items-center gap-3 border-t border-border-soft pt-5">
            <Button
              variant="primary"
              icon={<Play className="h-4 w-4" />}
              onClick={onRun}
              loading={running}
              disabled={!!blocked || missing.length > 0}
              title={blocked ?? (missing.length > 0 ? `Fill in: ${missing.map((f) => f.label).join(", ")}` : undefined)}
            >
              Run as {ctx.actor.displayName || "…"}
            </Button>
            {blocked && <span className="text-sm text-text-faint">{blocked}</span>}
            {!blocked && missing.length > 0 && (
              <span className="text-sm text-text-faint">Fill in {missing.map((f) => f.label.toLowerCase()).join(", ")}.</span>
            )}
          </div>
        )}
        {feature.notes && <p className="mt-4 text-sm text-text-faint">{feature.notes}</p>}
      </Panel>

      {lastRecord && <ResultBlock record={lastRecord} />}

      <SnippetBlock feature={feature} values={values} ctx={ctx} />
    </div>
  );
}

function FieldInput({
  field,
  value,
  onChange,
  userOptions,
  recent,
  wide,
}: {
  field: Field;
  value: Values[string];
  onChange: (v: Values[string]) => void;
  userOptions: UserOption[];
  recent: RecentIds;
  wide: boolean;
}) {
  const str = value === undefined || value === null ? "" : String(value);
  const listId = `pg-list-${field.key}-${field.type}`;

  let control: React.ReactNode;
  switch (field.type) {
    case "textarea":
      control = <Textarea value={str} onChange={(e) => onChange(e.target.value)} placeholder={field.placeholder} required={field.required} />;
      break;
    case "json":
      control = (
        <Textarea value={str} onChange={(e) => onChange(e.target.value)} placeholder={field.placeholder ?? "{ }"} className="font-mono text-[13px]" />
      );
      break;
    case "number":
      control = <Input type="number" value={str} onChange={(e) => onChange(e.target.value)} placeholder={field.placeholder} step="any" />;
      break;
    case "datetime":
      control = <Input type="datetime-local" value={str} onChange={(e) => onChange(e.target.value)} />;
      break;
    case "select":
      control = (
        <Select value={str} onChange={(e) => onChange(e.target.value)}>
          {(field.options ?? []).map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </Select>
      );
      break;
    case "checkbox":
      control = (
        <div className="flex h-[46px] items-center gap-3">
          <Switch checked={value === true} onChange={(next) => onChange(next)} label={field.label} />
          <span className="text-sm text-text-muted">{value === true ? "Yes" : "No"}</span>
        </div>
      );
      break;
    case "user":
      control = (
        <>
          <Input list={listId} value={str} onChange={(e) => onChange(e.target.value)} placeholder="user_id — pick from the list or paste one" className="font-mono text-[13px]" />
          <datalist id={listId}>
            {userOptions.map((u) => (
              <option key={u.userId} value={u.userId}>
                {u.label}
              </option>
            ))}
          </datalist>
        </>
      );
      break;
    case "recent": {
      const latest = field.recent ? recent[field.recent] : undefined;
      control = (
        <Input
          value={str}
          onChange={(e) => onChange(e.target.value)}
          placeholder={latest ? `latest: ${latest}` : `${field.recent ?? "id"} id`}
          className="font-mono text-[13px]"
        />
      );
      break;
    }
    default:
      control = <Input value={str} onChange={(e) => onChange(e.target.value)} placeholder={field.placeholder} required={field.required} />;
  }

  return (
    <div className={cx(wide && "sm:col-span-2")}>
      <Label>
        {field.label}
        {field.required && <span className="ml-1 text-danger">*</span>}
      </Label>
      {control}
      {field.hint && <p className="mt-1.5 text-xs text-text-faint">{field.hint}</p>}
    </div>
  );
}

export function statusTone(status: number): "success" | "warning" | "danger" | "default" {
  if (status === 0) return "success";
  if (status < 0) return "danger";
  if (status < 300) return "success";
  if (status < 500) return "warning";
  return "danger";
}

export function specLabel(spec: RequestSpec): string {
  return spec.kind === "ws" ? `WS ${String(spec.frame.type ?? "frame")}` : `${spec.method} ${spec.path}`;
}

function ResultBlock({ record }: { record: RequestRecord }) {
  const result = record.result;
  const status = result?.status ?? -1;
  return (
    <Panel animate={false} className="p-5">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Badge tone={statusTone(status)}>{status === 0 ? "sent" : status < 0 ? "error" : status}</Badge>
        <span className="truncate font-mono text-[13px] text-text">{specLabel(record.spec)}</span>
        <span className="ml-auto text-xs text-text-faint">
          as {record.actor.displayName}
          {result && result.durationMs > 0 && ` · ${result.durationMs} ms`}
        </span>
      </div>
      {record.spec.kind === "http" && record.spec.body !== undefined && (
        <details className="mb-3">
          <summary className="cursor-pointer text-xs text-text-faint hover:text-text">Request body</summary>
          <JsonView value={record.spec.body} className="mt-2" />
        </details>
      )}
      <JsonView value={result?.response ?? null} />
    </Panel>
  );
}

export function JsonView({ value, className }: { value: unknown; className?: string }) {
  const text = typeof value === "string" ? value : JSON.stringify(value, null, 2) ?? "null";
  return (
    <pre className={cx("max-h-96 overflow-auto rounded-xl border border-border bg-surface-2 p-4 font-mono text-xs leading-relaxed text-text", className)}>
      <code>{text}</code>
    </pre>
  );
}

function SnippetBlock({ feature, values, ctx }: { feature: Feature; values: Values; ctx: FeatureContext }) {
  const toast = useToast();
  const [lang, setLang] = useState<SnippetLang>("sdk");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    try {
      const stored = window.localStorage.getItem(SNIPPET_LANG_KEY);
      if (SNIPPET_LANGS.some((l) => l.id === stored)) setLang(stored as SnippetLang);
    } catch {
      // ignore
    }
  }, []);

  function choose(next: SnippetLang) {
    setLang(next);
    try {
      window.localStorage.setItem(SNIPPET_LANG_KEY, next);
    } catch {
      // ignore
    }
  }

  const spec = feature.build ? feature.build(values, ctx) : null;
  let code: string;
  let footnote: string;
  if (lang === "sdk") {
    if (feature.sdk) {
      code = feature.sdk(values, ctx);
      footnote = "Node.js, using the official @chat-platform/sdk package. USER_TOKEN comes from your backend's endUsers.create call.";
    } else if (spec) {
      code = spec.kind === "http" ? httpSnippet("fetch", ctx.apiBase, spec) : wsSnippet("fetch", ctx.wsBase, spec);
      footnote = "The Node.js SDK doesn't wrap this endpoint yet — this is the equivalent REST call with fetch.";
    } else {
      code = "// Nothing to call — this feature is about what arrives on the realtime connection.";
      footnote = "";
    }
  } else if (spec) {
    code = spec.kind === "http" ? httpSnippet(lang, ctx.apiBase, spec) : wsSnippet(lang, ctx.wsBase, spec);
    footnote = "USER_TOKEN is the end-user's client token — minted server-side via POST /users, never shipped as an app secret.";
  } else {
    code = lang === "curl" ? "# Nothing to call — subscribe to the realtime connection instead (see the Node.js SDK tab)." : "// Nothing to call — subscribe to the realtime connection instead (see the Node.js SDK tab).";
    footnote = "";
  }

  function copy() {
    navigator.clipboard
      .writeText(code)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => toast("error", "Couldn't copy automatically — select and copy the text manually."));
  }

  return (
    <Panel animate={false} className="p-5">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div className="flex items-center gap-1.5">
          {SNIPPET_LANGS.map((l) => (
            <button
              key={l.id}
              onClick={() => choose(l.id)}
              className={cx(
                "rounded-lg px-3 py-1.5 text-[13px] transition-colors duration-150",
                lang === l.id ? "bg-accent-soft font-medium text-accent" : "text-text-muted hover:bg-surface-2 hover:text-text"
              )}
            >
              {l.label}
            </button>
          ))}
        </div>
        <a
          href="https://github.com/REPLACE_ME/chat-platform-sdk-js#readme"
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-xs text-text-faint transition-colors duration-150 hover:text-text"
        >
          SDK docs <ExternalLink className="h-3 w-3" />
        </a>
      </div>
      <div className="relative">
        <pre className="overflow-x-auto rounded-xl border border-border bg-surface-2 p-4 font-mono text-[13px] leading-relaxed text-text">
          <code>{code}</code>
        </pre>
        <button
          onClick={copy}
          className="absolute right-3 top-3 rounded-lg border border-border bg-surface p-1.5 text-text-faint transition-colors duration-150 hover:text-text"
          title="Copy snippet"
        >
          {copied ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
      </div>
      {footnote && <p className="mt-3 text-xs text-text-faint">{footnote}</p>}
    </Panel>
  );
}
