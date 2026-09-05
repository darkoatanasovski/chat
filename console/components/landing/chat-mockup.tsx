"use client";

import { useEffect, useRef, useState } from "react";
import { Check, CheckCheck, Paperclip, Plus, SendHorizontal, Smile } from "lucide-react";

// A product-shaped chat window for the hero that plays a real conversation:
// each outgoing message pops in, then advances sent → delivered → read as
// its ticks change; the other side shows a typing indicator before their
// reply lands; reactions pop on afterward. The whole thing loops. Drawn in
// the landing theme's own tokens (no images), and it collapses to a static,
// fully-read transcript when the visitor prefers reduced motion.

type Status = "sending" | "delivered" | "read";
type Reaction = { emoji: string; count: number };

type Item =
  | { id: number; kind: "out"; text: string; time: string; status: Status }
  | { id: number; kind: "in"; author: string; avatar: string; hue: number; text: string; time: string; reactions?: Reaction[] }
  | { id: number; kind: "typing"; avatar: string; hue: number };

// The scripted conversation, replayed on a loop. Kept as data so the
// timeline runner below stays a simple, generic interpreter.
type Step =
  | { t: "out"; text: string; time: string }
  | { t: "status"; status: Status }
  | { t: "typing"; avatar: string; hue: number }
  | { t: "in"; author: string; avatar: string; hue: number; text: string; time: string }
  | { t: "react"; reactions: Reaction[] }
  | { t: "wait" }
  | { t: "reset" };

const SCRIPT: { step: Step; after: number }[] = [
  { step: { t: "out", text: "Awesome! Can I see a couple of pictures?", time: "4:56 pm" }, after: 500 },
  { step: { t: "status", status: "delivered" }, after: 900 },
  { step: { t: "status", status: "read" }, after: 900 },
  { step: { t: "typing", avatar: "JL", hue: 205 }, after: 1600 },
  { step: { t: "in", author: "James Lee", avatar: "JL", hue: 205, text: "Sure! Sending them over now.", time: "4:56 pm" }, after: 700 },
  { step: { t: "react", reactions: [{ emoji: "😍", count: 1 }, { emoji: "🔥", count: 2 }] }, after: 1500 },
  { step: { t: "out", text: "Thanks! Looks good.", time: "4:57 pm" }, after: 500 },
  { step: { t: "status", status: "delivered" }, after: 850 },
  { step: { t: "status", status: "read" }, after: 900 },
  { step: { t: "typing", avatar: "T", hue: 320 }, after: 1700 },
  { step: { t: "in", author: "Tessa", avatar: "T", hue: 320, text: "Absolutely. Just send your address and I'll ship it out.", time: "4:58 pm" }, after: 2600 },
  { step: { t: "wait" }, after: 1400 },
  { step: { t: "reset" }, after: 650 },
];

// The steady-state transcript shown to no-JS visitors and anyone who prefers
// reduced motion — the last frame of the loop, everything already read.
const STATIC_ITEMS: Item[] = [
  { id: 1, kind: "out", text: "Awesome! Can I see a couple of pictures?", time: "4:56 pm", status: "read" },
  { id: 2, kind: "in", author: "James Lee", avatar: "JL", hue: 205, text: "Sure! Sending them over now.", time: "4:56 pm", reactions: [{ emoji: "😍", count: 1 }, { emoji: "🔥", count: 2 }] },
  { id: 3, kind: "out", text: "Thanks! Looks good.", time: "4:57 pm", status: "read" },
  { id: 4, kind: "in", author: "Tessa", avatar: "T", hue: 320, text: "Absolutely. Just send your address and I'll ship it out.", time: "4:58 pm" },
];

function applyStep(items: Item[], step: Step, nextId: () => number): Item[] {
  switch (step.t) {
    case "out":
      return [...items, { id: nextId(), kind: "out", text: step.text, time: step.time, status: "sending" }];
    case "status": {
      // Advance the most recent outgoing message's delivery state.
      const copy = [...items];
      for (let i = copy.length - 1; i >= 0; i--) {
        const it = copy[i];
        if (it.kind === "out") {
          copy[i] = { ...it, status: step.status };
          break;
        }
      }
      return copy;
    }
    case "typing":
      return [...items, { id: nextId(), kind: "typing", avatar: step.avatar, hue: step.hue }];
    case "in": {
      // The reply replaces the trailing typing indicator.
      const copy = items[items.length - 1]?.kind === "typing" ? items.slice(0, -1) : [...items];
      return [...copy, { id: nextId(), kind: "in", author: step.author, avatar: step.avatar, hue: step.hue, text: step.text, time: step.time }];
    }
    case "react": {
      const copy = [...items];
      for (let i = copy.length - 1; i >= 0; i--) {
        const it = copy[i];
        if (it.kind === "in") {
          copy[i] = { ...it, reactions: step.reactions };
          break;
        }
      }
      return copy;
    }
    case "reset":
      return [];
    case "wait":
    default:
      return items;
  }
}

function Avatar({ initials, hue }: { initials: string; hue: number }) {
  return (
    <span
      className="grid h-7 w-7 shrink-0 select-none place-items-center rounded-full text-[11px] font-semibold text-white"
      style={{ background: `linear-gradient(135deg, hsl(${hue} 70% 55%), hsl(${hue + 30} 65% 42%))` }}
      aria-hidden="true"
    >
      {initials}
    </span>
  );
}

function Ticks({ status }: { status: Status }) {
  if (status === "sending") {
    return <Check className="h-3 w-3 text-text-faint transition-colors duration-300" strokeWidth={2.5} />;
  }
  return (
    <CheckCheck
      className={`h-3 w-3 transition-colors duration-300 ${status === "read" ? "text-accent" : "text-text-faint"}`}
      strokeWidth={2.5}
    />
  );
}

export function ChatMockup() {
  const [items, setItems] = useState<Item[]>([]);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    if (reduce) {
      setItems(STATIC_ITEMS);
      return;
    }

    let cancelled = false;
    let idCounter = 0;
    const nextId = () => (idCounter += 1);
    let timer: ReturnType<typeof setTimeout>;

    const runFrom = (index: number) => {
      if (cancelled) return;
      const { step, after } = SCRIPT[index];
      setItems((prev) => applyStep(prev, step, nextId));
      timer = setTimeout(() => runFrom((index + 1) % SCRIPT.length), after);
    };

    // Small beat before the first message so the empty window reads as
    // "about to receive" rather than broken.
    timer = setTimeout(() => runFrom(0), 400);

    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, []);

  // Keep the newest message in view as the log grows past the window height.
  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [items]);

  return (
    <div className="mx-auto w-full max-w-md">
      <div className="overflow-hidden rounded-[26px] border border-border bg-surface shadow-2xl">
        {/* Header */}
        <div className="flex items-center gap-3 border-b border-border-soft px-4 py-3.5">
          <Avatar initials="OS" hue={150} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-[14px] font-semibold text-text">Innovative Online Shopping</div>
            <div className="flex items-center gap-1.5 text-[11px] text-text-muted">
              <span className="h-1.5 w-1.5 rounded-full bg-accent" />
              8 members · 3 online
            </div>
          </div>
        </div>

        {/* Messages — fixed height, pinned to the bottom, oldest scroll out the top */}
        <div ref={logRef} className="flex h-[360px] flex-col justify-end gap-3 overflow-hidden px-4 py-5">
          {items.map((m) =>
            m.kind === "out" ? (
              <div key={m.id} className="chat-item flex flex-col items-end">
                <div className="max-w-[78%] rounded-2xl rounded-br-md bg-accent px-3.5 py-2 text-[13px] leading-snug text-bg">
                  {m.text}
                </div>
                <div className="mt-1 flex items-center gap-1 pr-1 text-[10px] text-text-faint">
                  {m.time}
                  <Ticks status={m.status} />
                </div>
              </div>
            ) : m.kind === "in" ? (
              <div key={m.id} className="chat-item flex items-end gap-2">
                <Avatar initials={m.avatar} hue={m.hue} />
                <div className="flex max-w-[78%] flex-col items-start">
                  <span className="mb-1 pl-1 text-[11px] font-medium" style={{ color: `hsl(${m.hue} 60% 62%)` }}>
                    {m.author}
                  </span>
                  <div className="rounded-2xl rounded-bl-md bg-surface-2 px-3.5 py-2 text-[13px] leading-snug text-text">
                    {m.text}
                  </div>
                  <div className="mt-1 flex items-center gap-1.5 pl-1">
                    {m.reactions?.length ? (
                      <span className="chat-pop inline-flex items-center gap-1 rounded-full border border-border bg-bg px-1.5 py-0.5">
                        {m.reactions.map((r) => (
                          <span key={r.emoji} className="flex items-center gap-0.5 text-[11px] text-text-muted">
                            <span className="text-[12px] leading-none">{r.emoji}</span>
                            {r.count}
                          </span>
                        ))}
                      </span>
                    ) : null}
                    <span className="text-[10px] text-text-faint">{m.time}</span>
                  </div>
                </div>
              </div>
            ) : (
              <div key={m.id} className="chat-item flex items-center gap-2">
                <Avatar initials={m.avatar} hue={m.hue} />
                <div className="flex items-center gap-1 rounded-2xl rounded-bl-md bg-surface-2 px-3.5 py-2.5">
                  <span className="chat-typing-dot h-1.5 w-1.5 rounded-full bg-text-faint" style={{ animationDelay: "0ms" }} />
                  <span className="chat-typing-dot h-1.5 w-1.5 rounded-full bg-text-faint" style={{ animationDelay: "150ms" }} />
                  <span className="chat-typing-dot h-1.5 w-1.5 rounded-full bg-text-faint" style={{ animationDelay: "300ms" }} />
                </div>
              </div>
            ),
          )}
        </div>

        {/* Input row */}
        <div className="flex items-center gap-2 border-t border-border-soft px-3 py-3">
          <button type="button" aria-label="Add" className="grid h-8 w-8 shrink-0 place-items-center rounded-full text-text-muted">
            <Plus className="h-[18px] w-[18px]" />
          </button>
          <div className="flex flex-1 items-center gap-2 rounded-full border border-border bg-bg px-3.5 py-2">
            <span className="flex-1 truncate text-[13px] text-text-faint">Type your message…</span>
            <Paperclip className="h-[17px] w-[17px] shrink-0 text-text-faint" />
            <Smile className="h-[17px] w-[17px] shrink-0 text-text-faint" />
          </div>
          <span className="grid h-9 w-9 shrink-0 place-items-center rounded-full bg-accent text-bg">
            <SendHorizontal className="h-[17px] w-[17px]" />
          </span>
        </div>
      </div>
    </div>
  );
}
