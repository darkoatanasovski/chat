"use client";

import {
  useEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import { animate, motion, useMotionValue, useTransform } from "framer-motion";
import { Check, Loader2, X } from "lucide-react";
import { twMerge } from "tailwind-merge";

// twMerge (not plain concatenation) because every component below accepts a
// `className` override on top of its own base classes.
export function cx(...parts: Array<string | false | null | undefined>) {
  return twMerge(parts.filter(Boolean).join(" "));
}

export function Panel({
  children,
  className,
  animate = true,
}: {
  children: ReactNode;
  className?: string;
  animate?: boolean;
}) {
  return (
    <div
      className={cx("rounded-2xl border border-border bg-surface p-7", animate && "animate-fade-in-up", className)}
    >
      {children}
    </div>
  );
}

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "primary" | "secondary" | "danger" | "ghost";
  loading?: boolean;
  icon?: ReactNode;
}

const variantClasses: Record<NonNullable<ButtonProps["variant"]>, string> = {
  primary: "bg-accent text-bg font-semibold hover:bg-accent-strong",
  secondary: "bg-surface-2 text-text border border-border hover:border-text-faint",
  danger: "bg-danger/90 text-bg font-semibold hover:bg-danger",
  ghost: "text-text-muted hover:text-text hover:bg-surface-2",
};

export function Button({ variant = "secondary", loading, icon, className, children, disabled, ...rest }: ButtonProps) {
  return (
    <button
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-xl px-4 py-2.5 text-[15px] transition-colors duration-150",
        "active:scale-[0.97] disabled:cursor-not-allowed disabled:opacity-40 disabled:active:scale-100",
        variantClasses[variant],
        className
      )}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : icon}
      {children}
    </button>
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...props}
      className={cx(
        "w-full rounded-xl border border-border bg-surface-2 px-3.5 py-2.5 text-[15px] text-text placeholder:text-text-faint",
        "outline-none transition-colors duration-150 focus:border-accent/60",
        props.className
      )}
    />
  );
}

export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      {...props}
      className={cx(
        "w-full rounded-xl border border-border bg-surface-2 px-3.5 py-2.5 text-[15px] text-text placeholder:text-text-faint",
        "min-h-[84px] resize-y outline-none transition-colors duration-150 focus:border-accent/60",
        props.className
      )}
    />
  );
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      {...props}
      className={cx(
        "w-full rounded-xl border border-border bg-surface-2 px-3.5 py-2.5 text-[15px] text-text",
        "outline-none transition-colors duration-150 focus:border-accent/60",
        props.className
      )}
    />
  );
}

export function Label({ children }: { children: ReactNode }) {
  return <label className="mb-2 block text-sm font-medium text-text-muted">{children}</label>;
}

// Switch is a toggle control — the "Channel Capabilities" panel's building
// block (console/app/console/apps/[id]/page.tsx's SettingsTab) but generic
// enough for any other on/off setting. Uncontrolled internally: the parent
// always owns `checked` and reacts to onChange, same pattern as a native
// checkbox, so a parent can debounce/persist however it needs to (see
// SettingsTab's optimistic-update-then-PATCH approach).
export function Switch({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  label?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        "relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors duration-150",
        "disabled:cursor-not-allowed disabled:opacity-40",
        checked ? "bg-accent" : "bg-surface-2 border border-border"
      )}
    >
      <motion.span
        layout
        transition={{ type: "spring", stiffness: 500, damping: 32 }}
        className={cx("block h-4.5 w-4.5 rounded-full bg-bg shadow-sm", checked ? "ml-[22px]" : "ml-1")}
      />
    </button>
  );
}

type BadgeTone = "default" | "success" | "danger" | "warning" | "accent";

const badgeToneClasses: Record<BadgeTone, string> = {
  default: "border-border text-text-muted",
  success: "border-success/30 bg-success-soft text-success",
  danger: "border-danger/30 bg-danger-soft text-danger",
  warning: "border-warning/30 bg-warning-soft text-warning",
  accent: "border-accent/30 bg-accent-soft text-accent",
};

export function Badge({
  children,
  tone = "default",
  icon,
  className,
}: {
  children: ReactNode;
  tone?: BadgeTone;
  icon?: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 rounded-full border px-3 py-1 font-mono text-xs font-medium",
        badgeToneClasses[tone],
        className
      )}
    >
      {icon}
      {children}
    </span>
  );
}

// StatusDot renders a small filled circle for online/offline presence —
// green when online, a faint neutral tone otherwise. The ring uses the
// current background color so it reads cleanly whether it's placed inline
// or overlapping an avatar (surface/surface-2 both pass a matching
// `ringClassName`).
export function StatusDot({
  online,
  ringClassName = "ring-surface",
  className,
}: {
  online: boolean;
  ringClassName?: string;
  className?: string;
}) {
  return (
    <span
      className={cx(
        "block h-2.5 w-2.5 shrink-0 rounded-full ring-2",
        online ? "bg-success" : "bg-text-faint/50",
        ringClassName,
        className
      )}
      title={online ? "Online" : "Offline"}
    />
  );
}

// formatLastSeen turns a UserStatus into the short label shown next to a
// StatusDot — "Online" while within the server's online window, otherwise
// "Last seen …" relative to last_active_at, or "Never active" when this
// user has no tracked activity at all yet.
export function formatLastSeen(status: { is_online: boolean; last_active_at?: string }): string {
  if (status.is_online) return "Online";
  if (!status.last_active_at) return "Never active";

  const then = new Date(status.last_active_at).getTime();
  const seconds = Math.max(0, Math.floor((Date.now() - then) / 1000));
  if (seconds < 60) return "Last seen just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `Last seen ${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `Last seen ${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `Last seen ${days}d ago`;
  return `Last seen ${new Date(status.last_active_at).toLocaleDateString()}`;
}

export function ErrorBanner({ children }: { children: ReactNode }) {
  return (
    <div className="animate-fade-in-up flex items-start gap-2.5 rounded-xl border border-danger/30 bg-danger-soft px-4 py-3 text-[15px] text-danger">
      {children}
    </div>
  );
}

export function Modal({
  open,
  onClose,
  title,
  icon,
  children,
  widthClass = "max-w-md",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  icon?: ReactNode;
  children: ReactNode;
  widthClass?: string;
}) {
  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div className="animate-overlay-in absolute inset-0 bg-black/70 backdrop-blur-sm" onClick={onClose} />
      <div
        className={cx(
          "animate-modal-in relative flex max-h-[85vh] w-full flex-col rounded-2xl border border-border bg-surface shadow-2xl",
          widthClass
        )}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="flex shrink-0 items-center justify-between border-b border-border-soft px-6 py-5">
          <div className="flex items-center gap-3">
            {icon}
            <h2 className="text-lg font-semibold text-text">{title}</h2>
          </div>
          <button
            onClick={onClose}
            className="rounded-lg p-1.5 text-text-muted transition-colors duration-150 hover:bg-surface-2 hover:text-text"
            aria-label="Close"
          >
            <X className="h-4.5 w-4.5" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto px-6 py-6">{children}</div>
      </div>
    </div>
  );
}

/** Wraps a settings row/panel so the global search palette
 * (components/global-search.tsx) can land on it: when `active` flips true —
 * because the URL's `?setting=` param matches `id` — this scrolls the row
 * into view and gives it a brief accent flash, then settles back to plain.
 * The `data-setting-key` attribute is there mainly for debugging/tests;
 * scrolling itself uses the ref, not a DOM query. */
export function SettingHighlight({
  id,
  active,
  className,
  children,
}: {
  id: string;
  active: boolean;
  className?: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [flash, setFlash] = useState(false);

  useEffect(() => {
    if (!active) return;
    ref.current?.scrollIntoView({ behavior: "smooth", block: "center" });
    setFlash(true);
    const t = setTimeout(() => setFlash(false), 2200);
    return () => clearTimeout(t);
  }, [active]);

  return (
    <div
      ref={ref}
      data-setting-key={id}
      className={cx(
        "rounded-xl transition-colors duration-700",
        flash && "bg-accent-soft ring-2 ring-accent/50",
        className
      )}
    >
      {children}
    </div>
  );
}

// Dark console theme: light-ish text on a translucent tinted dark
// background reads clearly; the light-theme equivalent (dark text on a
// pale tint) would go the other way if this theme flips back to light.
const avatarPalette = [
  "bg-blue-500/20 text-blue-300 ring-blue-500/25",
  "bg-violet-500/20 text-violet-300 ring-violet-500/25",
  "bg-emerald-500/20 text-emerald-300 ring-emerald-500/25",
  "bg-amber-500/20 text-amber-300 ring-amber-500/25",
  "bg-rose-500/20 text-rose-300 ring-rose-500/25",
  "bg-cyan-500/20 text-cyan-300 ring-cyan-500/25",
];

function paletteFor(seed: string) {
  let hash = 0;
  for (let i = 0; i < seed.length; i++) hash = (hash * 31 + seed.charCodeAt(i)) >>> 0;
  return avatarPalette[hash % avatarPalette.length];
}

const avatarSizeClasses = { sm: "h-9 w-9 text-xs", md: "h-11 w-11 text-sm", lg: "h-12 w-12 text-base" };

export function Avatar({ name, size = "md", className }: { name: string; size?: keyof typeof avatarSizeClasses; className?: string }) {
  const label = name || "?";
  return (
    <span
      className={cx(
        "grid shrink-0 place-items-center rounded-full font-mono font-semibold ring-1 ring-inset",
        avatarSizeClasses[size],
        paletteFor(label),
        className
      )}
    >
      {label.slice(0, 1).toUpperCase()}
    </span>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cx("animate-shimmer rounded-lg", className)} />;
}

/** A minimal 7-day trend line for an app card's "quick preview" of message
 * volume (see console's Apps page) — no axes, gridlines or legend, matching
 * a sparkline's job as a glance-and-move-on shape rather than a chart to
 * read values off of; the exact numbers sit next to it as Total/Today. A
 * day with zero messages is still a plotted point, never an omitted one —
 * an app that sent nothing this week still draws a flat baseline across
 * all `values.length` days rather than an empty box, so "no activity" is
 * visibly a fact about the week, not a missing chart. */
export function Sparkline({ values, height = 40, className }: { values: number[]; height?: number; className?: string }) {
  // The line spans the card's full, responsive width, but the coordinate
  // space has to stay square-scaled or the end-cap dot renders as a
  // stretched ellipse: previously the SVG used a fixed 120-unit viewBox with
  // preserveAspectRatio="none", so the x-axis was scaled up to fill the card
  // while the y-axis wasn't, distorting the <circle>. Instead we measure the
  // real pixel width and draw in that same 1:1 space, so the dot is always a
  // true circle and the stroke is uniform (no non-scaling-stroke hack).
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(120);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setWidth(el.clientWidth || 120);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const padX = 4;
  const padY = 5;
  const innerW = Math.max(width - padX * 2, 1);
  const innerH = height - padY * 2;
  const n = Math.max(values.length, 1);
  const stepX = n > 1 ? innerW / (n - 1) : 0;
  const max = Math.max(...values, 0);
  const baseline = height - padY;

  const points = values.map((v, i) => ({
    x: padX + i * stepX,
    y: max === 0 ? baseline : padY + innerH - (v / max) * innerH,
  }));

  const linePath = points.map((p, i) => `${i === 0 ? "M" : "L"} ${p.x.toFixed(2)} ${p.y.toFixed(2)}`).join(" ");
  const areaPath =
    points.length > 0
      ? `${linePath} L ${points[points.length - 1].x.toFixed(2)} ${baseline} L ${points[0].x.toFixed(2)} ${baseline} Z`
      : "";
  const last = points[points.length - 1];

  return (
    <div ref={ref} className={cx("block w-full", className)}>
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        className="block overflow-visible"
        role="img"
        aria-label={
          max === 0
            ? `No messages in the last ${values.length} days`
            : `Messages for the last ${values.length} days, most recent ${values[values.length - 1]}`
        }
      >
        {areaPath && <path d={areaPath} fill="var(--color-accent)" opacity={0.1} stroke="none" />}
        <path d={linePath} fill="none" stroke="var(--color-accent)" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" />
        {last && <circle cx={last.x} cy={last.y} r={3.5} fill="var(--color-accent)" stroke="var(--color-surface)" strokeWidth={2} />}
      </svg>
    </div>
  );
}

/** Counts up from 0 to `value` whenever it changes — used anywhere a raw
 * stat number appears (usage/overview stat cards) so updates read as live. */
export function AnimatedNumber({ value, className }: { value: number; className?: string }) {
  const motionValue = useMotionValue(0);
  const rounded = useTransform(motionValue, (v) => Math.round(v));
  const [display, setDisplay] = useState(0);

  useEffect(() => {
    const controls = animate(motionValue, value, { duration: 0.7, ease: [0.16, 1, 0.3, 1] });
    const unsubscribe = rounded.on("change", setDisplay);
    return () => {
      controls.stop();
      unsubscribe();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  return <span className={className}>{display}</span>;
}

/** The step indicator for the signup → first-app onboarding wizard (shared
 * between /signup and Overview's zero-apps state so both feel like one
 * continuous flow even though they're separate pages). Completed steps show
 * a checkmark, the active step is outlined, upcoming steps are faint. */
export function WizardProgress({ step, total, labels }: { step: number; total: number; labels: string[] }) {
  return (
    <div className="mb-8 flex items-center justify-center">
      {Array.from({ length: total }).map((_, i) => {
        const n = i + 1;
        const done = n < step;
        const active = n === step;
        return (
          <div key={n} className="flex items-center">
            <div className="flex flex-col items-center gap-1.5">
              <motion.div
                initial={{ scale: 0.6, opacity: 0 }}
                animate={{ scale: 1, opacity: 1 }}
                transition={{ delay: i * 0.08, type: "spring", stiffness: 400, damping: 22 }}
                className={cx(
                  "grid h-7 w-7 shrink-0 place-items-center rounded-full border text-xs font-semibold",
                  done && "border-accent bg-accent text-bg",
                  active && "border-accent bg-accent-soft text-accent",
                  !done && !active && "border-border text-text-faint"
                )}
              >
                {done ? <Check className="h-3.5 w-3.5" /> : n}
              </motion.div>
              <span className={cx("whitespace-nowrap text-[11px]", active ? "text-text" : "text-text-faint")}>{labels[i]}</span>
            </div>
            {n < total && <div className={cx("mb-4 h-px w-10 sm:w-14", done ? "bg-accent" : "bg-border")} />}
          </div>
        );
      })}
    </div>
  );
}
