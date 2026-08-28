"use client";

import { useEffect, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";
import { Loader2, X } from "lucide-react";
import { twMerge } from "tailwind-merge";

// twMerge (not plain concatenation) because every component below accepts a
// `className` override on top of its own base classes — plain string
// concatenation can silently lose depending on Tailwind's generated CSS
// order, not JSX order, e.g. a caller's `px-4` losing to a component's own
// base `px-3`. twMerge resolves same-property conflicts by keeping the
// last-specified class, matching what callers actually expect.
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
      className={cx(
        "rounded-xl border border-border bg-surface p-5 shadow-[0_1px_0_0_rgba(255,255,255,0.02)_inset]",
        animate && "animate-fade-in-up",
        className
      )}
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
  primary: "bg-accent text-white font-semibold hover:bg-accent-strong shadow-[0_0_0_1px_rgb(79_125_251/0.35)]",
  secondary:
    "bg-surface-2 text-text border border-border hover:border-text-faint hover:bg-white/[0.03]",
  danger: "bg-danger/90 text-bg font-semibold hover:bg-danger",
  ghost: "text-text-muted hover:text-text hover:bg-white/[0.04]",
};

export function Button({ variant = "secondary", loading, icon, className, children, disabled, ...rest }: ButtonProps) {
  return (
    <button
      className={cx(
        "inline-flex items-center justify-center gap-1.5 rounded-lg px-3.5 py-2 text-sm transition-all duration-150",
        "active:scale-[0.97] disabled:cursor-not-allowed disabled:opacity-40 disabled:active:scale-100",
        variantClasses[variant],
        className
      )}
      disabled={disabled || loading}
      {...rest}
    >
      {loading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : icon}
      {children}
    </button>
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...props}
      className={cx(
        "rounded-lg border border-border bg-surface-2 px-3 py-2 text-sm text-text placeholder:text-text-faint",
        "outline-none transition-colors duration-150 focus:border-accent/60 focus:ring-2 focus:ring-accent/15",
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
        "rounded-lg border border-border bg-surface-2 px-3 py-2 text-sm text-text",
        "outline-none transition-colors duration-150 focus:border-accent/60 focus:ring-2 focus:ring-accent/15",
        props.className
      )}
    />
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

const badgeSizeClasses = {
  sm: "px-2 py-0.5 text-[10px]",
  md: "px-2.5 py-1 text-[11px]",
};

export function Badge({
  children,
  tone = "default",
  size = "md",
  icon,
  pulse,
  className,
}: {
  children: ReactNode;
  tone?: BadgeTone;
  size?: keyof typeof badgeSizeClasses;
  icon?: ReactNode;
  pulse?: boolean;
  className?: string;
}) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 rounded-full border font-mono font-medium",
        badgeSizeClasses[size],
        badgeToneClasses[tone],
        className
      )}
    >
      {pulse && (
        <span className="relative flex h-1.5 w-1.5">
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-current opacity-60" />
          <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-current" />
        </span>
      )}
      {icon}
      {children}
    </span>
  );
}

export function ErrorBanner({ children }: { children: ReactNode }) {
  return (
    <div className="animate-fade-in-up flex items-start gap-2.5 rounded-lg border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger">
      {children}
    </div>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <code className="rounded-md border border-border bg-surface-2 px-1.5 py-0.5 font-mono text-[12px] text-text">
      {children}
    </code>
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
      <div className="animate-overlay-in absolute inset-0 bg-black/65 backdrop-blur-sm" onClick={onClose} />
      <div
        className={cx(
          "animate-modal-in relative flex max-h-[85vh] w-full flex-col rounded-2xl border border-border bg-surface shadow-2xl",
          widthClass
        )}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="flex shrink-0 items-center justify-between border-b border-border-soft px-6 py-4">
          <div className="flex items-center gap-2.5">
            {icon}
            <h2 className="text-base font-semibold text-text">{title}</h2>
          </div>
          <button
            onClick={onClose}
            className="rounded-lg p-1.5 text-text-muted transition-colors duration-150 hover:bg-white/[0.06] hover:text-text"
            aria-label="Close"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto px-6 py-5">{children}</div>
      </div>
    </div>
  );
}

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

const avatarSizeClasses = {
  sm: "h-8 w-8 text-xs",
  md: "h-10 w-10 text-sm",
  lg: "h-11 w-11 text-base",
};

const avatarAccentClasses = "bg-accent-soft text-accent ring-accent/25";

export function Avatar({
  name,
  size = "md",
  online,
  /** Reserved for "you" — a fixed, branded color instead of the
   * hash-derived palette, so your own identity reads consistently instead
   * of landing on a random, possibly-clashing hue. */
  accent,
  className,
}: {
  name: string;
  size?: keyof typeof avatarSizeClasses;
  online?: boolean;
  accent?: boolean;
  className?: string;
}) {
  const label = name || "?";
  return (
    <span className={cx("relative inline-flex shrink-0", className)}>
      <span
        className={cx(
          "grid place-items-center rounded-full font-mono font-semibold ring-1 ring-inset",
          avatarSizeClasses[size],
          accent ? avatarAccentClasses : paletteFor(label)
        )}
      >
        {label.slice(0, 1).toUpperCase()}
      </span>
      {online !== undefined && (
        <span
          className={cx(
            "absolute -bottom-0.5 -right-0.5 h-2.5 w-2.5 rounded-full ring-2 ring-surface",
            online ? "bg-success" : "bg-text-faint"
          )}
        />
      )}
    </span>
  );
}
