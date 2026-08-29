"use client";

import { useEffect, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";
import { animate, useMotionValue, useTransform } from "framer-motion";
import { Loader2, X } from "lucide-react";
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
